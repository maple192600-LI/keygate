package service

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"github.com/tabloy/keygate/internal/auth"
	keycrypto "github.com/tabloy/keygate/internal/crypto"
	"github.com/tabloy/keygate/internal/offline"
	"github.com/tabloy/keygate/internal/store"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func sourceBackupService(t *testing.T) (*OfflineBackupService, *OfflineIssuanceService, string, OfflineIssueResult, time.Time) {
	svc, actor, intent, base := offlineServiceFixture(t)
	first, err := svc.Issue(context.Background(), actor, prepareOfflineCommand(t, svc, intent))
	if err != nil {
		t.Fatal(err)
	}
	dsn, err := url.Parse(os.Getenv("SNAIL_LOCAL_TEST_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	var name string
	if err = svc.Store.DB.NewRaw("SELECT current_database()").Scan(context.Background(), &name); err != nil {
		t.Fatal(err)
	}
	dsn.Path = "/" + name
	backup := &OfflineBackupService{Store: svc.Store, Keys: svc.Keys, DatabaseURL: dsn.String(), DataDir: t.TempDir(), MigrationDir: "../../db/migrations", EncryptionSecret: strings.Repeat("07", 32)}
	return backup, svc, actor, first, base
}
func emptySnapshotTarget(t *testing.T, migrationDir string) *store.Store {
	ctx := context.Background()
	dsn := os.Getenv("SNAIL_LOCAL_TEST_DSN")
	if dsn == "" {
		t.Skip("task-owned PostgreSQL required")
	}
	parent, err := store.New(dsn)
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	name := "snail_bundle_" + strings.ReplaceAll(store.NewID(), "-", "")
	if _, err = parent.DB.ExecContext(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	s, err := store.New(u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
		if _, err := parent.DB.ExecContext(ctx, "DROP DATABASE "+name); err != nil {
			t.Error(err)
		}
		if err := parent.Close(); err != nil {
			t.Error(err)
		}
	})
	if err = s.RunMigrations(migrationDir); err != nil {
		t.Fatal(err)
	}
	return s
}
func blankBackupTarget(t *testing.T) (*store.Store, *offline.KeyStore, string) {
	ctx := context.Background()
	s := emptySnapshotTarget(t, "../../db/migrations")
	s.LicenseKeyAEAD = keycrypto.MustDeriveAEAD(bytes.Repeat([]byte{9}, 32), "license-key")
	hash, err := auth.HashPassword("Synthetic-target-owner-password")
	if err != nil {
		t.Fatal(err)
	}
	owner, err := s.InitializeLocal(ctx, "target_owner", hash, "合成空白目标厂商", "合成联系方式")
	if err != nil {
		t.Fatal(err)
	}
	keys := offline.NewKeyStore(t.TempDir())
	t.Cleanup(keys.Lock)
	return s, keys, owner.ID
}

func TestOfflineRestoreRejectsIssuanceRollbackAtConfirmation(t *testing.T) {
	ctx := context.Background()
	backup, svc, actor, first, base := sourceBackupService(t)
	raw, err := backup.Backup(ctx, "Synthetic-backup-continuity", true, true)
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := backup.PreviewRestore(ctx, raw, "Synthetic-backup-continuity")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := backup.DiscardRestore(ctx, candidate); err != nil {
			t.Error(err)
		}
	}()
	if err = backup.ValidateRestoreContinuity(ctx, candidate); err != nil {
		t.Fatal(err)
	}
	for revision := int64(1); revision <= 2; revision++ {
		intent := OfflineIntent{Action: "renew", LicenseID: first.LicenseID, ExpectedRevision: revision, Term: offline.Term{Kind: "days", Amount: 7}}
		if _, err = svc.Issue(ctx, actor, prepareOfflineCommand(t, svc, intent)); err != nil {
			t.Fatal(err)
		}
		if err = backup.ValidateRestoreContinuity(ctx, candidate); err == nil {
			t.Fatal("old preview erased newer issuance")
		}
	}
	if got := countOfflineRows(t, svc.Store, "license_issuances"); got != 3 {
		t.Fatal("source changed", got)
	}
	latest, err := backup.Backup(ctx, "Synthetic-backup-continuity", true, true)
	if err != nil {
		t.Fatal(err)
	}
	current, err := backup.PreviewRestore(ctx, latest, "Synthetic-backup-continuity")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := backup.DiscardRestore(ctx, current); err != nil {
			t.Error(err)
		}
	}()
	if err = backup.ValidateRestoreContinuity(ctx, current); err != nil {
		t.Fatal(err)
	}
	keys := offline.NewKeyStore(current.IdentityRoot)
	defer keys.Lock()
	if _, err = keys.Unlock("Synthetic-service-signing-password", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	restored := &OfflineIssuanceService{Store: current.Store, Keys: keys, PreviewSecret: bytes.Repeat([]byte{5}, 32), clock: func() time.Time { return base }}
	next, err := restored.Issue(ctx, actor, prepareOfflineCommand(t, restored, OfflineIntent{Action: "renew", LicenseID: first.LicenseID, ExpectedRevision: 3, Term: offline.Term{Kind: "days", Amount: 7}}))
	if err != nil || next.Revision != 4 {
		t.Fatal("trusted latest restore did not continue at r4", next, err)
	}
}

func TestOfflineRegisteredPublicIdentityConstrainsZeroIssuance(t *testing.T) {
	ctx := context.Background()
	target, keys, _ := blankBackupTarget(t)
	source := offline.NewKeyStore(t.TempDir())
	defer source.Lock()
	public, err := source.Create("Synthetic-zero-history-key", time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if err = target.SetSetting(ctx, "snail_public_identity", mustJSON(public)); err != nil {
		t.Fatal(err)
	}
	expected, err := target.ExpectedSigningIdentity(ctx, offline.PublicIdentity{})
	if err != nil || expected.KeyID != public.KeyID {
		t.Fatal("zero-issuance expected key lost", err)
	}
	wrong := offline.NewKeyStore(t.TempDir())
	defer wrong.Lock()
	wrongPublic, err := wrong.Create("Synthetic-wrong-history-key", time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = target.ExpectedSigningIdentity(ctx, wrongPublic); err == nil {
		t.Fatal("registered A replaced by B")
	}
	original, err := source.EncryptedIdentity()
	if err != nil {
		t.Fatal(err)
	}
	if err = keys.Import(original, expected.KeyID); err != nil {
		t.Fatal(err)
	}
	physical, err := keys.Unlock("Synthetic-zero-history-key", time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = target.ExpectedSigningIdentity(ctx, physical); err != nil {
		t.Fatal(err)
	}
}
func TestOfflineBusinessTransferPreservesOriginalsAndRenews(t *testing.T) {
	ctx := context.Background()
	backup, source, actor, first, base := sourceBackupService(t)
	original, err := source.Store.LicenseIssuance(ctx, first.IssuanceID)
	if err != nil {
		t.Fatal(err)
	}
	delivered, _, err := source.Store.RecordOfflineDelivery(ctx, &store.OfflineDelivery{IssuanceID: first.IssuanceID, OperationID: store.NewID(), Method: "usb", Recipient: "合成客户", DeliveredAt: base, CreatedBy: actor})
	if err != nil {
		t.Fatal(err)
	}
	enabled := true
	if _, err = source.Store.SetOfflineIssuanceState(ctx, actor, "license", first.LicenseID, store.OfflineStateCommand{OperationID: store.NewID(), Enabled: false, ExpectedEnabled: &enabled, ExpectedRevision: 1, Reason: "合成移交前停发"}); err != nil {
		t.Fatal(err)
	}
	if _, err = source.Store.SetOfflineIssuanceState(ctx, actor, "customer", intentCustomer(t, source.Store, first.LicenseID), store.OfflineStateCommand{OperationID: store.NewID(), Enabled: false, ExpectedEnabled: &enabled, Reason: "合成客户停发"}); err != nil {
		t.Fatal(err)
	}
	raw, err := backup.ExportBusiness(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"password_hash", "refresh_tokens", "ciphertextB64", "license_key_encrypted", "jwtSecret", "encryptionSecret"} {
		if bytes.Contains(raw, []byte(`"`+forbidden+`"`)) {
			t.Fatalf("business export contains %s", forbidden)
		}
	}
	bundle, err := store.ParseBusinessBundle(raw)
	if err != nil {
		t.Fatal(err)
	}
	target, keys, targetActor := blankBackupTarget(t)
	summary, err := target.ImportBusinessBundle(ctx, bundle, targetActor, false)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Customers != 1 || summary.Licenses != 1 || summary.Originals != 1 || len(summary.Mappings) != 2 {
		t.Fatal("preview counts/mapping", summary)
	}
	if countOfflineRows(t, target, "commercial_customers") != 0 || countOfflineRows(t, target, "license_issuances") != 0 {
		t.Fatal("preview wrote business data")
	}
	if _, err = target.ImportBusinessBundle(ctx, bundle, targetActor, true); err != nil {
		t.Fatal(err)
	}
	got, err := target.LicenseIssuance(ctx, first.IssuanceID)
	if err != nil || !bytes.Equal(got.RawBytes, original.RawBytes) || got.SHA256 != original.SHA256 || got.LicenseID != original.LicenseID || got.Revision != 1 {
		t.Fatal("original identity/bytes changed", err)
	}
	deliveries, err := target.OfflineDeliveries(ctx, first.IssuanceID)
	if err != nil || len(deliveries) != 1 || deliveries[0].ID != delivered.ID || deliveries[0].CreatedBy != actor {
		t.Fatal("delivery provenance changed", err)
	}
	var accounts int
	if err = target.DB.NewRaw("SELECT count(*) FROM users").Scan(ctx, &accounts); err != nil || accounts != 1 {
		t.Fatal("source login imported", err)
	}
	if _, err = target.ImportBusinessBundle(ctx, bundle, targetActor, true); err == nil {
		t.Fatal("nonempty target accepted second import")
	}
	identity, err := source.Keys.EncryptedIdentity()
	if err != nil {
		t.Fatal(err)
	}
	if err = keys.Import(identity, bundle.PublicIdentity.KeyID); err != nil {
		t.Fatal(err)
	}
	if _, err = keys.Unlock("Synthetic-service-signing-password", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	replacement := &OfflineIssuanceService{Store: target, Keys: keys, PreviewSecret: bytes.Repeat([]byte{4}, 32), clock: func() time.Time { return base }}
	renew := OfflineIntent{Action: "renew", LicenseID: first.LicenseID, ExpectedRevision: 1, Term: offline.Term{Kind: "days", Amount: 7}}
	if _, err = replacement.Preview(ctx, renew); err == nil {
		t.Fatal("stopped state was lost during import")
	}
	disabled := false
	if _, err = target.SetOfflineIssuanceState(ctx, targetActor, "license", first.LicenseID, store.OfflineStateCommand{OperationID: store.NewID(), Enabled: true, ExpectedEnabled: &disabled, ExpectedRevision: 1, Reason: "合成移交后恢复"}); err != nil {
		t.Fatal(err)
	}
	if _, err = target.SetOfflineIssuanceState(ctx, targetActor, "customer", intentCustomer(t, target, first.LicenseID), store.OfflineStateCommand{OperationID: store.NewID(), Enabled: true, ExpectedEnabled: &disabled, Reason: "合成客户恢复"}); err != nil {
		t.Fatal(err)
	}
	next, err := replacement.Issue(ctx, targetActor, prepareOfflineCommand(t, replacement, renew))
	if err != nil {
		t.Fatal(err)
	}
	if next.Revision != 2 || next.LicenseID != first.LicenseID || next.ExpiresAt == nil || *next.ExpiresAt != base.Add(14*24*time.Hour).Format("2006-01-02T15:04:05.000Z") {
		t.Fatal("target renewal changed contract", next)
	}
	renewed, err := target.LicenseIssuance(ctx, next.IssuanceID)
	if err != nil {
		t.Fatal(err)
	}
	pub, err := offline.ParsePublic([]byte(bundle.PublicIdentity.PublicKeyPEM))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = offline.Verify(renewed.RawBytes, map[string]ed25519.PublicKey{bundle.PublicIdentity.KeyID: pub}, offline.ProductID); err != nil {
		t.Fatal(err)
	}
	// Malformed relations and plan definition conflicts leave a fresh target empty.
	for _, kind := range []string{"hash", "revision", "relationship", "plan", "unknown-column"} {
		t.Run(kind, func(t *testing.T) {
			fresh, _, owner := blankBackupTarget(t)
			b, err := store.ParseBusinessBundle(raw)
			if err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "hash":
				b.Tables["license_issuances"][0]["sha256"] = json.RawMessage(`"` + strings.Repeat("0", 64) + `"`)
			case "revision":
				b.Tables["licenses"][0]["current_revision"] = json.RawMessage(`2`)
			case "relationship":
				b.Tables["license_deliveries"][0]["issuance_id"] = json.RawMessage(`"` + store.NewID() + `"`)
			case "plan":
				b.Tables["plans"][0]["max_activations"] = json.RawMessage(`12`)
			case "unknown-column":
				b.Tables["commercial_customers"][0]["password_hash"] = json.RawMessage(`"forbidden"`)
			}
			if _, err = fresh.ImportBusinessBundle(ctx, b, owner, true); err == nil {
				t.Fatal("malformed bundle accepted")
			}
			if countOfflineRows(t, fresh, "commercial_customers") != 0 || countOfflineRows(t, fresh, "licenses") != 0 {
				t.Fatal("failed import left partial business rows")
			}
		})
	}
}
func intentCustomer(t *testing.T, s *store.Store, licenseID string) string {
	t.Helper()
	var id string
	if err := s.DB.NewRaw("SELECT customer_id FROM licenses WHERE id=?", licenseID).Scan(context.Background(), &id); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestOfflineActorMigrationPreservesEarlierTaskIssuance(t *testing.T) {
	ctx := context.Background()
	_, source, actor, first, _ := sourceBackupService(t)
	snapshot, err := source.Store.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Model the earlier T03/T07 schema with the same synthetic data. This is a
	// schema-upgrade regression, not a customer migration/import product feature.
	delete(snapshot.Tables, "offline_actors")
	history := snapshot.Tables["schema_migrations"][:0]
	for _, row := range snapshot.Tables["schema_migrations"] {
		var name string
		_ = json.Unmarshal(row["filename"], &name)
		if name < "20261024" {
			history = append(history, row)
		}
	}
	snapshot.Tables["schema_migrations"] = history
	previousDir := t.TempDir()
	files, err := os.ReadDir("../../db/migrations")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range files {
		if entry.IsDir() || entry.Name() >= "20261024" || !strings.HasSuffix(entry.Name(), ".up.sql") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join("../../db/migrations", entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(previousDir, entry.Name()), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	previous := emptySnapshotTarget(t, previousDir)
	if err = previous.RestoreSnapshot(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	before, err := previous.LicenseIssuance(ctx, first.IssuanceID)
	if err != nil {
		t.Fatal(err)
	}
	if err = previous.RunMigrations("../../db/migrations"); err != nil {
		t.Fatal(err)
	}
	var name string
	if err = previous.DB.NewRaw("SELECT display_name FROM offline_actors WHERE id=?", actor).Scan(ctx, &name); err != nil || name != "service_owner" {
		t.Fatal("actor backfill", name, err)
	}
	after, err := previous.LicenseIssuance(ctx, first.IssuanceID)
	if err != nil || !bytes.Equal(before.RawBytes, after.RawBytes) || before.SHA256 != after.SHA256 || before.CreatedBy != after.CreatedBy {
		t.Fatal("upgrade changed signed original or provenance", err)
	}
}
func TestOfflineEncryptedBackupPreviewAndFailureProtection(t *testing.T) {
	ctx := context.Background()
	backup, source, _, first, _ := sourceBackupService(t)
	password := "Synthetic-independent-backup-password"
	if _, err := backup.Backup(ctx, password, true, false); err == nil {
		t.Fatal("identity confirmation not required")
	}
	raw, err := backup.Backup(ctx, password, true, true)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("password_hash")) || bytes.Contains(raw, []byte(backup.EncryptionSecret)) {
		t.Fatal("backup plaintext exposed")
	}
	if _, err = backup.PreviewRestore(ctx, raw, "Synthetic-wrong-password"); err == nil {
		t.Fatal("wrong password accepted")
	}
	var c offline.BackupContainer
	if err = json.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	firstChar := "A"
	if strings.HasPrefix(c.CiphertextB64, firstChar) {
		firstChar = "B"
	}
	c.CiphertextB64 = firstChar + c.CiphertextB64[1:]
	corrupt, _ := json.Marshal(c)
	if _, err = backup.PreviewRestore(ctx, corrupt, password); err == nil {
		t.Fatal("corruption accepted")
	}
	candidate, err := backup.PreviewRestore(ctx, raw, password)
	if err != nil {
		t.Fatal(err)
	}
	if candidate.Summary.Customers != 1 || candidate.Summary.Licenses != 1 || candidate.Summary.Originals != 1 || !candidate.IncludesIdentity {
		t.Fatal("restore preview", candidate.Summary)
	}
	original, _ := source.Store.LicenseIssuance(ctx, first.IssuanceID)
	restored, err := candidate.Store.LicenseIssuance(ctx, first.IssuanceID)
	if err != nil || !bytes.Equal(original.RawBytes, restored.RawBytes) {
		t.Fatal("restored original mismatch", err)
	}
	restoredKeys := offline.NewKeyStore(candidate.IdentityRoot)
	if _, err = restoredKeys.Unlock("Synthetic-service-signing-password", time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	restoredKeys.Lock()
	candidateID := candidate.ID
	ownedRoot := candidate.IdentityRoot
	candidate.IdentityRoot = backup.DataDir
	if err = backup.DiscardRestore(ctx, candidate); err == nil {
		t.Fatal("unsafe candidate path accepted")
	}
	if err = candidate.Store.DB.PingContext(ctx); err != nil {
		t.Fatal("ownership rejection closed candidate before validation", err)
	}
	candidate.IdentityRoot = ownedRoot
	if err = backup.DiscardRestore(ctx, candidate); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(backup.DataDir, "restores", candidateID)); !os.IsNotExist(err) {
		t.Fatal("candidate cleanup not confirmed", err)
	}
	if countOfflineRows(t, source.Store, "commercial_customers") != 1 || countOfflineRows(t, source.Store, "license_issuances") != 1 {
		t.Fatal("preview affected source database")
	}
	// A self-consistent outer checksum cannot make an invalid FK or unknown
	// table safe; all schema and relations are validated in a new candidate.
	plain, err := offline.OpenBackup(raw, password)
	if err != nil {
		t.Fatal(err)
	}
	var payload FullBackupPayload
	if err = json.Unmarshal(plain, &payload); err != nil {
		t.Fatal(err)
	}
	var snapshot store.LocalSnapshot
	if err = json.Unmarshal(payload.Database, &snapshot); err != nil {
		t.Fatal(err)
	}
	snapshot.Tables["license_issuances"][0]["binding_id"] = json.RawMessage(`"` + store.NewID() + `"`)
	payload.Database, _ = json.Marshal(snapshot)
	payload.DatabaseSHA256 = digest(payload.Database)
	modified, _ := json.Marshal(payload)
	invalid, err := offline.SealBackup(modified, password)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = backup.PreviewRestore(ctx, invalid, password); err == nil {
		t.Fatal("invalid FK restored")
	}
	if err = json.Unmarshal(plain, &payload); err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(payload.Database, &snapshot); err != nil {
		t.Fatal(err)
	}
	snapshot.Tables["users"][0]["password_hash"] = json.RawMessage(`"corrupted-password-record"`)
	payload.Database, _ = json.Marshal(snapshot)
	payload.DatabaseSHA256 = digest(payload.Database)
	modified, _ = json.Marshal(payload)
	invalid, err = offline.SealBackup(modified, password)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = backup.PreviewRestore(ctx, invalid, password); err == nil || !strings.Contains(err.Error(), "密码记录") {
		t.Fatal("unusable owner credential record accepted", err)
	}
	without, err := backup.Backup(ctx, password, false, false)
	if err != nil {
		t.Fatal(err)
	}
	candidate, err = backup.PreviewRestore(ctx, without, password)
	if err != nil {
		t.Fatal(err)
	}
	if candidate.IncludesIdentity {
		t.Fatal("identity unexpectedly included")
	}
	if err = backup.DiscardRestore(ctx, candidate); err != nil {
		t.Fatal(err)
	}
}
