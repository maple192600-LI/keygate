package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tabloy/keygate/internal/auth"
	keycrypto "github.com/tabloy/keygate/internal/crypto"
	"github.com/tabloy/keygate/internal/offline"
	"github.com/tabloy/keygate/internal/store"
)

type FullBackupPayload struct {
	FormatVersion    int                    `json:"formatVersion"`
	Kind             string                 `json:"kind"`
	CreatedAt        string                 `json:"createdAt"`
	Database         json.RawMessage        `json:"database"`
	DatabaseSHA256   string                 `json:"databaseSha256"`
	EncryptionSecret string                 `json:"encryptionSecret"`
	PublicIdentity   offline.PublicIdentity `json:"publicIdentity"`
	IdentityFile     []byte                 `json:"identityFile"`
	IdentitySHA256   string                 `json:"identitySha256"`
	Summary          store.BundleSummary    `json:"summary"`
}
type RestoreCandidate struct {
	ID               string              `json:"previewId"`
	Summary          store.BundleSummary `json:"summary"`
	IncludesIdentity bool                `json:"includesIdentity"`
	ExpiresAt        time.Time           `json:"expiresAt"`
	Store            *store.Store        `json:"-"`
	DatabaseURL      string              `json:"-"`
	DatabaseName     string              `json:"-"`
	IdentityRoot     string              `json:"-"`
	EncryptionSecret string              `json:"-"`
}
type OfflineBackupService struct {
	Store            *store.Store
	Keys             *offline.KeyStore
	DatabaseURL      string
	DataDir          string
	MigrationDir     string
	EncryptionSecret string
}

type BusinessTransfer struct {
	Bundle       *store.BusinessBundle
	LicenseCodes map[string]string
}

type sealedBusinessTransfer struct {
	FormatVersion int                   `json:"formatVersion"`
	Kind          string                `json:"kind"`
	Bundle        *store.BusinessBundle `json:"bundle"`
	LicenseCodes  map[string]string     `json:"licenseCodes"`
}

func backupFailure(message string) error {
	return &offline.Error{Code: "BACKUP_INVALID", Message: message + "；当前资料保留"}
}
func digest(raw []byte) string { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }
func (s *OfflineBackupService) PublicIdentity(ctx context.Context) (offline.PublicIdentity, error) {
	p, err := s.Keys.Status()
	if err != nil {
		return p, err
	}
	return s.Store.ExpectedSigningIdentity(ctx, p)
}
func (s *OfflineBackupService) ExportBusiness(ctx context.Context, actor, password string) ([]byte, error) {
	p, err := s.PublicIdentity(ctx)
	if err != nil {
		return nil, err
	}
	bundleRaw, err := s.Store.ExportBusinessBundle(ctx, p)
	if err != nil {
		return nil, err
	}
	bundle, err := store.ParseBusinessBundle(bundleRaw)
	if err != nil {
		return nil, err
	}
	codes := make(map[string]string, len(bundle.Tables["licenses"]))
	for _, row := range bundle.Tables["licenses"] {
		var id string
		if err = json.Unmarshal(row["id"], &id); err != nil || id == "" {
			return nil, backupFailure("授权编号无效")
		}
		code, codeErr := s.Store.OfflineLicenseCode(ctx, actor, id)
		if codeErr != nil {
			return nil, codeErr
		}
		codes[id] = code
	}
	plain, err := json.Marshal(sealedBusinessTransfer{FormatVersion: 1, Kind: "snail-commercial-transfer", Bundle: bundle, LicenseCodes: codes})
	if err != nil {
		return nil, err
	}
	defer clear(plain)
	return offline.SealBackup(plain, password)
}

func (s *OfflineBackupService) OpenBusiness(raw []byte, password string) (*BusinessTransfer, error) {
	plain, err := offline.OpenBackup(raw, password)
	if err != nil {
		return nil, err
	}
	defer clear(plain)
	if _, err = offline.StrictJSON(plain, offline.BackupLimit); err != nil {
		return nil, err
	}
	var payload sealedBusinessTransfer
	decoder := json.NewDecoder(bytes.NewReader(plain))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&payload); err != nil || payload.FormatVersion != 1 || payload.Kind != "snail-commercial-transfer" || payload.Bundle == nil || payload.LicenseCodes == nil {
		return nil, backupFailure("业务移交包无效")
	}
	if err = store.ValidateBusinessBundle(payload.Bundle); err != nil {
		return nil, err
	}
	return &BusinessTransfer{Bundle: payload.Bundle, LicenseCodes: payload.LicenseCodes}, nil
}
func (s *OfflineBackupService) Backup(ctx context.Context, password string, includeIdentity, confirmed bool) ([]byte, error) {
	if includeIdentity && !confirmed {
		return nil, backupFailure("请明确确认备份包含加密签发身份")
	}
	public, err := s.PublicIdentity(ctx)
	if err != nil {
		return nil, err
	}
	bundleRaw, err := s.Store.ExportBusinessBundle(ctx, public)
	if err != nil {
		return nil, err
	}
	bundle, err := store.ParseBusinessBundle(bundleRaw)
	if err != nil {
		return nil, err
	}
	snapshot, err := s.Store.Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	database, err := json.Marshal(snapshot)
	if err != nil {
		return nil, err
	}
	var identity []byte
	if includeIdentity {
		identity, err = s.Keys.EncryptedIdentity()
		if err != nil {
			return nil, err
		}
		container, err := offline.ParseContainer(identity)
		if err != nil || container.KeyID != public.KeyID {
			return nil, backupFailure("签发身份与快照不一致")
		}
	}
	payload := FullBackupPayload{1, "snail-local-database", time.Now().UTC().Format(time.RFC3339Nano), database, digest(database), s.EncryptionSecret, public, identity, digest(identity), store.BundleSummary{Customers: len(bundle.Tables["commercial_customers"]), Licenses: len(bundle.Tables["licenses"]), Originals: len(bundle.Tables["license_issuances"]), KeyID: public.KeyID, Mappings: []store.BundleMapping{}}}
	plain, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	defer clear(plain)
	return offline.SealBackup(plain, password)
}
func (s *OfflineBackupService) PreviewRestore(ctx context.Context, raw []byte, password string) (_ *RestoreCandidate, resultErr error) {
	plain, err := offline.OpenBackup(raw, password)
	if err != nil {
		return nil, err
	}
	defer clear(plain)
	if _, err = offline.StrictJSON(plain, offline.BackupLimit); err != nil {
		return nil, err
	}
	var payload FullBackupPayload
	decoder := json.NewDecoder(bytes.NewReader(plain))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&payload); err != nil {
		return nil, backupFailure("备份清单无效")
	}
	master, err := hex.DecodeString(payload.EncryptionSecret)
	if err != nil || len(master) != 32 || payload.FormatVersion != 1 || payload.Kind != "snail-local-database" || digest(payload.Database) != payload.DatabaseSHA256 || digest(payload.IdentityFile) != payload.IdentitySHA256 {
		return nil, backupFailure("备份清单或摘要不一致")
	}
	if len(payload.IdentityFile) > 0 {
		container, err := offline.ParseContainer(payload.IdentityFile)
		if err != nil || container.KeyID != payload.PublicIdentity.KeyID || container.PublicKeyPEM != payload.PublicIdentity.PublicKeyPEM {
			return nil, backupFailure("备份签发身份不一致")
		}
	}
	var snapshot store.LocalSnapshot
	decoder = json.NewDecoder(bytes.NewReader(payload.Database))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&snapshot); err != nil {
		return nil, backupFailure("数据库快照无效")
	}
	parsed, err := url.Parse(s.DatabaseURL)
	if err != nil {
		return nil, err
	}
	id := store.NewID()
	name := "snail_restore_" + strings.ReplaceAll(id, "-", "")
	if _, err = s.Store.DB.ExecContext(ctx, "CREATE DATABASE "+name); err != nil {
		return nil, err
	}
	candidate := &RestoreCandidate{ID: id, ExpiresAt: time.Now().Add(10 * time.Minute), DatabaseName: name, EncryptionSecret: payload.EncryptionSecret, IncludesIdentity: len(payload.IdentityFile) > 0}
	parsed.Path = "/" + name
	candidate.DatabaseURL = parsed.String()
	defer func() {
		if resultErr != nil {
			if err := s.DiscardRestore(context.WithoutCancel(ctx), candidate); err != nil {
				resultErr = errors.Join(resultErr, err)
			}
		}
	}()
	candidate.Store, err = store.New(candidate.DatabaseURL)
	if err != nil {
		return nil, err
	}
	if err = candidate.Store.RunMigrations(s.MigrationDir); err != nil {
		return nil, err
	}
	if err = candidate.Store.RestoreSnapshot(ctx, &snapshot); err != nil {
		return nil, err
	}
	candidate.Store.LicenseKeyAEAD = keycrypto.MustDeriveAEAD(master, "license-key")
	bundleRaw, err := candidate.Store.ExportBusinessBundle(ctx, payload.PublicIdentity)
	if err != nil {
		return nil, err
	}
	bundle, err := store.ParseBusinessBundle(bundleRaw)
	if err != nil {
		return nil, err
	}
	candidate.Summary = store.BundleSummary{Customers: len(bundle.Tables["commercial_customers"]), Licenses: len(bundle.Tables["licenses"]), Originals: len(bundle.Tables["license_issuances"]), KeyID: payload.PublicIdentity.KeyID, Mappings: []store.BundleMapping{}}
	if candidate.Summary.Customers != payload.Summary.Customers || candidate.Summary.Licenses != payload.Summary.Licenses || candidate.Summary.Originals != payload.Summary.Originals || candidate.Summary.KeyID != payload.Summary.KeyID {
		return nil, backupFailure("备份数量与回读不一致")
	}
	var ownerCount int
	if err = candidate.Store.DB.NewRaw("SELECT count(*) FROM users WHERE role='owner' AND username IS NOT NULL AND password_hash IS NOT NULL").Scan(ctx, &ownerCount); err != nil {
		return nil, err
	}
	if ownerCount != 1 {
		return nil, backupFailure("备份没有唯一可登录管理账号")
	}
	var ownerHash string
	if err = candidate.Store.DB.NewRaw("SELECT password_hash FROM users WHERE role='owner'").Scan(ctx, &ownerHash); err != nil {
		return nil, err
	}
	if !auth.ValidPasswordHash(ownerHash) {
		return nil, backupFailure("备份管理账号的密码记录无效")
	}
	// Decrypt only the old internal KG credentials, never the signing identity.
	var licenses []struct {
		ID                  string
		LicenseKey          string
		LicenseKeyEncrypted []byte
	}
	if err = candidate.Store.DB.NewRaw("SELECT id,license_key,license_key_encrypted FROM licenses").Scan(ctx, &licenses); err != nil {
		return nil, err
	}
	for _, l := range licenses {
		value, err := candidate.Store.LicenseKeyAEAD.Decrypt(l.LicenseKeyEncrypted, []byte(l.ID))
		if err != nil || string(value) != l.LicenseKey {
			return nil, backupFailure("备份运行加密配置与数据库不配套")
		}
		clear(value)
	}
	restoreRoot := filepath.Join(s.DataDir, "restores")
	if err = os.MkdirAll(restoreRoot, 0700); err != nil {
		return nil, err
	}
	resolved, err := filepath.EvalSymlinks(restoreRoot)
	if err != nil || !strings.EqualFold(resolved, restoreRoot) {
		return nil, backupFailure("恢复资料目录不安全")
	}
	candidate.IdentityRoot = filepath.Join(restoreRoot, id)
	if err = os.Mkdir(candidate.IdentityRoot, 0700); err != nil {
		return nil, err
	}
	if len(payload.IdentityFile) > 0 {
		if err = offline.NewKeyStore(candidate.IdentityRoot).Import(payload.IdentityFile, payload.PublicIdentity.KeyID); err != nil {
			return nil, err
		}
	}
	if _, err = candidate.Store.ExpectedSigningIdentity(ctx, payload.PublicIdentity); err != nil {
		return nil, err
	}
	if err = candidate.Store.SetSetting(ctx, "snail_public_identity", mustJSON(payload.PublicIdentity)); err != nil {
		return nil, err
	}
	return candidate, nil
}
func mustJSON(v any) string { raw, _ := json.Marshal(v); return string(raw) }

// Called at confirmation while the local write gate is held. Preview age alone
// cannot protect originals issued after the preview or after an old backup.
func (s *OfflineBackupService) ValidateRestoreContinuity(ctx context.Context, c *RestoreCandidate, signingPassphrase string) error {
	if c == nil || c.Store == nil {
		return backupFailure("恢复候选不可用")
	}
	physical, err := s.Keys.Status()
	damagedIdentity := err != nil
	var expected offline.PublicIdentity
	if err != nil {
		// A damaged active container must not block a verified full recovery.
		// Only a registered public identity and a healthy replacement container
		// can authorize this path; normal signing still requires the active key.
		physicalErr := err
		expected, err = s.Store.ExpectedSigningIdentity(ctx, offline.PublicIdentity{})
		if err != nil || expected.KeyID == "" || expected.PublicKeyPEM == "" || !c.IncludesIdentity {
			return physicalErr
		}
	} else {
		expected, err = s.Store.ExpectedSigningIdentity(ctx, physical)
	}
	if err != nil {
		return err
	}
	restored, err := c.Store.ExpectedSigningIdentity(ctx, offline.PublicIdentity{})
	if err != nil {
		return err
	}
	if expected.KeyID != "" && expected.KeyID != restored.KeyID {
		return backupFailure("备份缺少当前已登记的签发身份")
	}
	// A missing container reports "uncreated" rather than an error. Replacing
	// that registered identity still requires proving the candidate can unlock.
	if physical.KeyID == "" && expected.KeyID != "" && c.IncludesIdentity {
		damagedIdentity = true
	}
	if damagedIdentity {
		keys := offline.NewKeyStore(c.IdentityRoot)
		defer keys.Lock()
		master, decodeErr := hex.DecodeString(c.EncryptionSecret)
		servicePassphrase, deriveErr := offline.ServicePassphrase(master)
		var identity offline.PublicIdentity
		var unlockErr error
		if decodeErr == nil && deriveErr == nil {
			identity, unlockErr = keys.UnlockService(servicePassphrase)
		}
		if unlockErr != nil && signingPassphrase != "" {
			identity, unlockErr = keys.Unlock(signingPassphrase, time.Now().Add(time.Minute))
		}
		if unlockErr != nil {
			if signingPassphrase == "" {
				return &offline.Error{Code: "RESTORE_SIGNING_PASSWORD_REQUIRED", Message: "当前签发身份文件需要核对原密码或使用服务签发身份备份后恢复"}
			}
			return unlockErr
		}
		if identity.KeyID != expected.KeyID {
			return backupFailure("备份中的签发身份与当前登记不一致")
		}
	}
	type fact struct {
		LicenseID string
		Revision  int64
		ID        string
		SHA256    string `bun:"sha256"`
	}
	var current, candidate []fact
	if err = s.Store.DB.NewRaw("SELECT license_id,revision,id,sha256 FROM license_issuances").Scan(ctx, &current); err != nil {
		return err
	}
	if err = c.Store.DB.NewRaw("SELECT license_id,revision,id,sha256 FROM license_issuances").Scan(ctx, &candidate); err != nil {
		return err
	}
	known := make(map[string]fact, len(candidate))
	for _, row := range candidate {
		known[row.ID] = row
	}
	for _, row := range current {
		if got, ok := known[row.ID]; !ok || got != row {
			return &offline.Error{Code: "RESTORE_ISSUANCE_ROLLBACK", Message: "备份缺少本机已签发原件或包含同版冲突，不能切换；请使用最新备份或先补齐签发记录"}
		}
	}
	return nil
}
func (s *OfflineBackupService) DiscardRestore(ctx context.Context, c *RestoreCandidate) error {
	expectedName := "snail_restore_" + strings.ReplaceAll(c.ID, "-", "")
	if !offline.ValidUUID4(c.ID) || c.DatabaseName != expectedName || c.Store == s.Store {
		return backupFailure("恢复候选数据库归属不明确，未清理")
	}
	parsed, err := url.Parse(c.DatabaseURL)
	if err != nil || parsed.Path != "/"+expectedName {
		return backupFailure("恢复候选连接不匹配，未清理")
	}
	removeDirectory := false
	if c.IdentityRoot != "" {
		expected := filepath.Join(s.DataDir, "restores", c.ID)
		if c.IdentityRoot != expected {
			return backupFailure("恢复候选目录归属不明确，未清理")
		}
		stat, err := os.Lstat(expected)
		if err == nil {
			resolved, e := filepath.EvalSymlinks(expected)
			if !stat.IsDir() || stat.Mode()&os.ModeSymlink != 0 || e != nil || !strings.EqualFold(resolved, expected) {
				return backupFailure("恢复候选路径不安全，未清理")
			}
			removeDirectory = true
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	var errs []error
	if c.Store != nil {
		if err := c.Store.Close(); err != nil {
			errs = append(errs, err)
		}
		c.Store = nil
	}
	if c.DatabaseName != "" {
		if err := s.Store.DropDatabase(ctx, c.DatabaseName); err != nil {
			errs = append(errs, err)
		}
	}
	if removeDirectory {
		if err := os.RemoveAll(c.IdentityRoot); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
