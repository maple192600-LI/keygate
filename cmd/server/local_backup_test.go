package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/tabloy/keygate/internal/config"
	"github.com/tabloy/keygate/internal/service"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func (a *localTestApp) upload(t *testing.T, path string, raw []byte, password string) (int, []byte) {
	t.Helper()
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	part, err := w.CreateFormFile("file", "synthetic-archive")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = part.Write(raw); err != nil {
		t.Fatal(err)
	}
	if password != "" {
		if err = w.WriteField("password", password); err != nil {
			t.Fatal(err)
		}
	}
	if err = w.Close(); err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest("POST", a.server.URL+path, &body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.Header.Set("Origin", a.server.URL)
	res, err := a.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	data, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return res.StatusCode, data
}
func TestLocalOfflineRestoreSwitchAndRuntimeFailurePreservesSource(t *testing.T) {
	s := localTestStore(t)
	t.Chdir("../..")
	server := httptest.NewUnstartedServer(nil)
	address := server.Listener.Addr().String()
	_, port, _ := strings.Cut(address, ":")
	dataRoot := t.TempDir()
	dsn, err := url.Parse(os.Getenv("SNAIL_LOCAL_TEST_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	var dbname string
	if err = s.DB.NewRaw("SELECT current_database()").Scan(context.Background(), &dbname); err != nil {
		t.Fatal(err)
	}
	dsn.Path = "/" + dbname
	cfg := &config.Config{Port: port, BaseURL: "http://" + address, Environment: "production", SnailDeployment: "local_offline", SnailDataDir: dataRoot, SnailRuntimeToken: strings.Repeat("c", 64), JWTSecret: strings.Repeat("b", 64), ReleaseKeyEncryptionKey: strings.Repeat("a", 64), DatabaseURL: dsn.String()}
	runtime := map[string]any{"formatVersion": 1, "databasePassword": strings.Repeat("d", 64), "databasePort": 55437, "appPort": 9000, "jwtSecret": cfg.JWTSecret, "encryptionSecret": cfg.ReleaseKeyEncryptionKey}
	initial, _ := json.Marshal(runtime)
	initialDigest := sha256.Sum256(initial)
	cfg.SnailRuntimeConfigSHA256 = hex.EncodeToString(initialDigest[:])
	runtimeFile := filepath.Join(dataRoot, "runtime.json")
	if err = os.WriteFile(runtimeFile, initial, 0600); err != nil {
		t.Fatal(err)
	}
	managed := newLocalRouterSwitch(cfg, s, func() {})
	server.Config.Handler = managed
	server.Start()
	jar, _ := cookiejar.New(nil)
	a := &localTestApp{server: server, client: &http.Client{Jar: jar}, store: s, runtimeToken: cfg.SnailRuntimeToken}
	t.Cleanup(func() {
		server.Close()
		if err := managed.Close(); err != nil {
			t.Error(err)
		}
		var selected map[string]any
		raw, err := os.ReadFile(runtimeFile)
		if err != nil {
			t.Error(err)
			return
		}
		if err = json.Unmarshal(raw, &selected); err != nil {
			t.Error(err)
			return
		}
		if name, ok := selected["databaseName"].(string); ok {
			if !strings.HasPrefix(name, "snail_restore_") {
				t.Error("unexpected cleanup database")
				return
			}
			if _, err = s.DB.ExecContext(context.Background(), "DROP DATABASE "+name); err != nil {
				t.Error(err)
			}
		}
	})
	ownerPassword := "Synthetic-restored-owner-password"
	newPassword := "Synthetic-current-owner-password"
	backupPassword := "Synthetic-archive-password-2026"
	if status, body := a.call(t, "POST", "/api/v1/local/setup", map[string]string{"username": "restore_owner", "password": ownerPassword, "siteName": "合成恢复厂商", "contact": "合成联系号"}); status != 201 {
		t.Fatalf("setup %d %s", status, body)
	}
	if status, body := a.call(t, "POST", "/api/v1/auth/password/login", map[string]string{"username": "restore_owner", "password": ownerPassword}); status != 200 {
		t.Fatalf("login %d %s", status, body)
	}
	status, body := a.call(t, "POST", "/api/v1/admin/commercial-customers", map[string]any{"displayName": "备份中的客户", "customerType": "company", "contactMethod": "合成联系号"})
	if status != 201 {
		t.Fatalf("customer %d %s", status, body)
	}
	customer := decodeLocal[struct {
		ID string `json:"id"`
	}](t, body)
	status, body = a.call(t, "POST", "/api/v1/admin/offline-key/create", map[string]string{"ownerPassword": ownerPassword, "passphrase": "Synthetic-restored-signing-password"})
	if status != 201 {
		t.Fatalf("key %d %s", status, body)
	}
	status, body = a.call(t, "POST", "/api/v1/admin/offline-requests", map[string]string{"deviceCode": "STQ-WIN1-" + strings.Repeat("a", 64), "deviceLabel": "合成恢复设备"})
	if status != 200 {
		t.Fatalf("request %d %s", status, body)
	}
	request := decodeLocal[struct {
		Request struct {
			ID string `json:"requestId"`
		} `json:"request"`
	}](t, body)
	intent := map[string]any{"action": "initial", "customerId": customer.ID, "requestId": request.Request.ID, "term": map[string]any{"kind": "days", "amount": 7}}
	status, body = a.call(t, "POST", "/api/v1/admin/offline-licenses/preview", intent)
	if status != 200 {
		t.Fatalf("preview %d %s", status, body)
	}
	preview := decodeLocal[service.OfflinePreview](t, body)
	intent["operationId"] = "ca51470a-2a89-4d6e-a1ee-93dd073ca5b0"
	intent["previewBase"] = preview.PreviewBase
	status, body = a.call(t, "POST", "/api/v1/admin/offline-licenses", intent)
	if status != 200 {
		t.Fatalf("issue %d %s", status, body)
	}
	parsedURL, _ := url.Parse(server.URL)
	oldCookies := a.client.Jar.Cookies(parsedURL)
	status, archive := a.call(t, "POST", "/api/v1/admin/backups", map[string]any{"password": backupPassword, "includeIdentity": true, "confirmIdentity": true})
	if status != 200 {
		t.Fatalf("backup %d %s", status, archive)
	}
	if status, body = a.call(t, "POST", "/api/v1/auth/password/change", map[string]string{"currentPassword": ownerPassword, "newPassword": newPassword}); status != 200 {
		t.Fatalf("change %d %s", status, body)
	}
	if status, body = a.call(t, "POST", "/api/v1/auth/password/login", map[string]string{"username": "restore_owner", "password": newPassword}); status != 200 {
		t.Fatalf("new login %d %s", status, body)
	}
	if status, body = a.call(t, "POST", "/api/v1/admin/commercial-customers", map[string]string{"displayName": "备份之后新增客户", "customerType": "person"}); status != 201 {
		t.Fatalf("later customer %d %s", status, body)
	}
	if status, _ = a.upload(t, "/api/v1/admin/backups/preview", archive, "Wrong-synthetic-backup-password"); status == 200 {
		t.Fatal("wrong backup password accepted")
	}
	status, body = a.upload(t, "/api/v1/admin/backups/preview", archive, backupPassword)
	if status != 200 {
		t.Fatalf("restore preview %d %s", status, body)
	}
	restore := decodeLocal[struct {
		ID      string `json:"previewId"`
		Summary struct {
			Customers int `json:"customers"`
			Originals int `json:"originals"`
		} `json:"summary"`
	}](t, body)
	if restore.Summary.Customers != 1 || restore.Summary.Originals != 1 {
		t.Fatal("preview does not match snapshot")
	}
	// An unexpected runtime configuration must prevent publication; source still
	// has both customers and its current password/session remains usable.
	confirm := map[string]string{"previewId": restore.ID, "ownerPassword": newPassword}
	var count int
	for _, field := range []string{"jwtSecret", "databasePassword", "databasePort", "identityDirectory"} {
		var changedRuntime map[string]any
		_ = json.Unmarshal(initial, &changedRuntime)
		switch field {
		case "databasePort":
			changedRuntime[field] = 55438
		case "identityDirectory":
			changedRuntime[field] = "restores/ca51470a-2a89-4d6e-a1ee-93dd073ca5b0"
		default:
			changedRuntime[field] = strings.Repeat("e", 64)
		}
		changed, _ := json.Marshal(changedRuntime)
		if err = os.WriteFile(runtimeFile, changed, 0600); err != nil {
			t.Fatal(err)
		}
		if status, _ = a.call(t, "POST", "/api/v1/admin/backups/restore", confirm); status == 200 {
			t.Fatalf("%s conflict did not prevent switch", field)
		}
		if status, _ = a.call(t, "GET", "/api/v1/auth/me", nil); status != 200 {
			t.Fatalf("%s conflict revoked current session", field)
		}
		if err = s.DB.NewRaw("SELECT count(*) FROM commercial_customers").Scan(context.Background(), &count); err != nil || count != 2 {
			t.Fatal("failed restore changed source", field, count, err)
		}
	}
	if err = os.WriteFile(runtimeFile, initial, 0600); err != nil {
		t.Fatal(err)
	}
	if status, body = a.call(t, "POST", "/api/v1/admin/backups/restore", confirm); status != 200 {
		t.Fatalf("confirm %d %s", status, body)
	}
	if status, _ = a.call(t, "GET", "/api/v1/auth/me", nil); status != 401 {
		t.Fatal("old current session survived restored database")
	}
	oldJar, _ := cookiejar.New(nil)
	oldJar.SetCookies(parsedURL, oldCookies)
	oldClient := &localTestApp{server: server, client: &http.Client{Jar: oldJar}}
	if status, _ = oldClient.call(t, "POST", "/api/v1/auth/refresh", nil); status != 401 {
		t.Fatal("backed-up refresh session survived restore")
	}
	// The archived password is restored, not the newer source password.
	if status, _ = a.call(t, "POST", "/api/v1/auth/password/login", map[string]string{"username": "restore_owner", "password": newPassword}); status != 401 {
		t.Fatal("source password leaked across restore")
	}
	if status, body = a.call(t, "POST", "/api/v1/auth/password/login", map[string]string{"username": "restore_owner", "password": ownerPassword}); status != 200 {
		t.Fatalf("restored login %d %s", status, body)
	}
	status, body = a.call(t, "GET", "/api/v1/admin/commercial-customers", nil)
	if status != 200 {
		t.Fatalf("customers %d %s", status, body)
	}
	var reply struct {
		Data struct {
			Items []struct {
				DisplayName string `json:"displayName"`
			} `json:"items"`
		} `json:"data"`
	}
	if err = json.Unmarshal(body, &reply); err != nil || len(reply.Data.Items) != 1 || reply.Data.Items[0].DisplayName != "备份中的客户" {
		t.Fatal("router did not select restored data", err)
	}
	raw, err := os.ReadFile(runtimeFile)
	if err != nil {
		t.Fatal(err)
	}
	var saved map[string]any
	_ = json.Unmarshal(raw, &saved)
	if saved["databaseName"] == nil || saved["identityDirectory"] == nil || saved["jwtSecret"] == cfg.JWTSecret {
		t.Fatal("restore not persisted for native next launch")
	}
	savedDigest := sha256.Sum256(raw)
	if managed.cfg.SnailRuntimeConfigSHA256 != hex.EncodeToString(savedDigest[:]) {
		t.Fatal("next restore does not use newly published runtime digest")
	}
	if err = managed.owned[len(managed.owned)-1].DB.NewRaw("SELECT count(*) FROM audit_logs WHERE entity='backup' AND entity_id=? AND action='restore'", restore.ID).Scan(context.Background(), &count); err != nil || count != 1 {
		t.Fatal("restore retry duplicated audit", count, err)
	}
	if err = s.DB.NewRaw("SELECT count(*) FROM commercial_customers").Scan(context.Background(), &count); err != nil || count != 2 {
		t.Fatal("old source database not preserved", count, err)
	}
}
