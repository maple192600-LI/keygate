package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/tabloy/keygate/internal/config"
	"github.com/tabloy/keygate/internal/offline"
	"github.com/tabloy/keygate/internal/service"
	"github.com/tabloy/keygate/internal/store"
)

// Keep the same listener and native launch capability while switching to the
// fully validated DB. Requests already admitted to the old router are drained
// by its stopped gate; old databases and files stay intact.
type localRouterSwitch struct {
	current  atomic.Pointer[gin.Engine]
	mu       sync.Mutex
	cfg      *config.Config
	shutdown func()
	cleanups []func(context.Context) error
	owned    []*store.Store
}

func newLocalRouterSwitch(cfg *config.Config, db *store.Store, shutdown func()) *localRouterSwitch {
	r := &localRouterSwitch{cfg: cfg, shutdown: shutdown}
	router, closeRouter := newManagedLocalRouter(cfg, db, r.activate, shutdown)
	r.current.Store(router)
	r.cleanups = append(r.cleanups, closeRouter)
	return r
}
func (r *localRouterSwitch) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	r.current.Load().ServeHTTP(w, req)
}
func (r *localRouterSwitch) activate(candidate *service.RestoreCandidate) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := candidate.Store.DB.Ping(); err != nil {
		return err
	}
	next := *r.cfg
	next.DatabaseURL = candidate.DatabaseURL
	next.SnailIdentityDir = candidate.IdentityRoot
	next.ReleaseKeyEncryptionKey = candidate.EncryptionSecret
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return err
	}
	next.JWTSecret = hex.EncodeToString(secret)
	router, closeRouter := newManagedLocalRouter(&next, candidate.Store, r.activate, r.shutdown)
	if err := persistRestoreSelection(r.cfg, &next, candidate.DatabaseName); err != nil {
		_ = closeRouter(context.Background())
		return err
	}
	r.current.Store(router)
	r.cfg = &next
	r.cleanups = append(r.cleanups, closeRouter)
	r.owned = append(r.owned, candidate.Store)
	return nil
}
func (r *localRouterSwitch) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var errs []error
	for _, fn := range r.cleanups {
		if err := fn(ctx); err != nil {
			errs = append(errs, err)
		}
	}
	for _, db := range r.owned {
		if err := db.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
func persistRestoreSelection(previous, next *config.Config, databaseName string) error {
	if !strings.HasPrefix(databaseName, "snail_restore_") || len(databaseName) != len("snail_restore_")+32 {
		return &offline.Error{Code: "RESTORE_RUNTIME_INVALID", Message: "恢复数据库身份无效"}
	}
	file := filepath.Join(previous.SnailDataDir, "runtime.json")
	stat, err := os.Lstat(file)
	if err != nil || !stat.Mode().IsRegular() || stat.Mode()&os.ModeSymlink != 0 || stat.Size() > 16384 {
		return &offline.Error{Code: "RESTORE_RUNTIME_INVALID", Message: "无法核对本机运行配置；当前资料保留"}
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(raw)
	if previous.SnailRuntimeConfigSHA256 != hex.EncodeToString(digest[:]) {
		return &offline.Error{Code: "RESTORE_RUNTIME_INVALID", Message: "运行配置已变化，未切换现有资料"}
	}
	if _, err = offline.StrictJSON(raw, 16384); err != nil {
		return err
	}
	var value map[string]any
	if err = json.Unmarshal(raw, &value); err != nil {
		return err
	}
	if value["formatVersion"] != float64(1) || value["encryptionSecret"] != previous.ReleaseKeyEncryptionKey || value["jwtSecret"] != previous.JWTSecret {
		return &offline.Error{Code: "RESTORE_RUNTIME_INVALID", Message: "运行配置已变化，未切换现有资料"}
	}
	relative, err := filepath.Rel(previous.SnailDataDir, next.SnailIdentityDir)
	if err != nil || filepath.IsAbs(relative) || strings.HasPrefix(relative, "..") {
		return &offline.Error{Code: "RESTORE_RUNTIME_INVALID", Message: "恢复身份目录超出本机资料根"}
	}
	value["databaseName"] = databaseName
	value["identityDirectory"] = filepath.ToSlash(relative)
	value["encryptionSecret"] = next.ReleaseKeyEncryptionKey
	value["jwtSecret"] = next.JWTSecret
	updated, err := json.Marshal(value)
	if err != nil {
		return err
	}
	temp, err := os.CreateTemp(previous.SnailDataDir, "runtime-restore-*.tmp")
	if err != nil {
		return err
	}
	name := temp.Name()
	published := false
	defer func() {
		if !published {
			_ = os.Remove(name)
		}
	}()
	if err = temp.Chmod(0600); err == nil {
		_, err = temp.Write(updated)
	}
	if err == nil {
		err = temp.Sync()
	}
	closeErr := temp.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = os.Rename(name, file); err != nil {
		return err
	}
	published = true
	digest = sha256.Sum256(updated)
	next.SnailRuntimeConfigSHA256 = hex.EncodeToString(digest[:])
	return nil
}
