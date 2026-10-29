package service

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"github.com/tabloy/keygate/internal/auth"
	keycrypto "github.com/tabloy/keygate/internal/crypto"
	"github.com/tabloy/keygate/internal/model"
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

func licenseCodesForBundle(t *testing.T, ctx context.Context, s *store.Store, actor string, bundle *store.BusinessBundle) map[string]string {
	t.Helper()
	codes := make(map[string]string, len(bundle.Tables["licenses"]))
	for _, row := range bundle.Tables["licenses"] {
		var id string
		if err := json.Unmarshal(row["id"], &id); err != nil {
			t.Fatal("read bundle license id", err)
		}
		code, err := s.OfflineLicenseCode(ctx, actor, id)
		if err != nil {
			t.Fatal("read protected source license code", err)
		}
		codes[id] = code
	}
	return codes
}

func TestOfflineBusinessBundlePreservesUnactivatedLicenseFacts(t *testing.T) {
	ctx := context.Background()
	svc, actor, _, base := offlineServiceFixture(t)
	var customerID string
	if err := svc.Store.DB.NewRaw("SELECT id FROM commercial_customers LIMIT 1").Scan(ctx, &customerID); err != nil {
		t.Fatal(err)
	}
	startDate := base.In(time.FixedZone("Asia/Shanghai", 8*3600)).Format("2006-01-02")
	type licenseFacts struct {
		ID          string `bun:"id"`
		DeviceLimit int    `bun:"device_limit"`
		TermKind    string `bun:"term_kind"`
		TermAmount  int    `bun:"term_amount"`
		EndDate     string `bun:"end_date"`
		StartsAt    string `bun:"starts_at"`
	}
	expected := map[string]licenseFacts{}
	for _, test := range []struct {
		deviceLimit int
		term        offline.Term
		endDate     string
	}{
		{deviceLimit: 3, term: offline.Term{Kind: "days", Amount: 17, StartsAt: startDate}, endDate: ""},
		{deviceLimit: 2, term: offline.Term{Kind: "date", EndDate: "2026-11-30", StartsAt: startDate}, endDate: "2026-11-30"},
	} {
		created, err := svc.Issue(ctx, actor, prepareOfflineCommand(t, svc, OfflineIntent{
			Action: "initial", CustomerID: customerID, DeviceLimit: test.deviceLimit, Term: test.term,
		}))
		if err != nil {
			t.Fatal("create unactivated code", err)
		}
		expected[created.LicenseID] = licenseFacts{ID: created.LicenseID, DeviceLimit: test.deviceLimit, TermKind: test.term.Kind, TermAmount: test.term.Amount, EndDate: test.endDate, StartsAt: startDate}
	}
	var source []licenseFacts
	if err := svc.Store.DB.NewRaw(`SELECT id,device_limit,term_kind,term_amount,
COALESCE(to_char(term_end_date,'YYYY-MM-DD'),'') AS end_date,
COALESCE(to_char(term_starts_at AT TIME ZONE 'Asia/Shanghai','YYYY-MM-DD'),'') AS starts_at
FROM licenses WHERE fulfillment_mode='offline_file' ORDER BY id`).Scan(ctx, &source); err != nil {
		t.Fatal("read source license facts", err)
	}
	if len(source) != len(expected) {
		t.Fatalf("source license count = %d, want %d", len(source), len(expected))
	}
	for _, facts := range source {
		if want := expected[facts.ID]; facts != want {
			t.Fatalf("source license facts differ from the chosen terms: got %+v, want %+v", facts, want)
		}
	}
	identity, err := svc.Keys.Status()
	if err != nil {
		t.Fatal(err)
	}
	identity.State = "public"
	identityRaw, _ := json.Marshal(identity)
	if err = svc.Store.SetSetting(ctx, "snail_public_identity", string(identityRaw)); err != nil {
		t.Fatal(err)
	}
	bundleRaw, err := svc.Store.ExportBusinessBundle(ctx, identity)
	if err != nil {
		t.Fatal("export unactivated codes", err)
	}
	bundle, err := store.ParseBusinessBundle(bundleRaw)
	if err != nil {
		t.Fatal("parse unactivated codes", err)
	}
	codes := licenseCodesForBundle(t, ctx, svc.Store, actor, bundle)
	target, _, targetActor := blankBackupTarget(t)
	if _, err = target.ImportBusinessBundle(ctx, bundle, codes, targetActor, true); err != nil {
		t.Fatal("import unactivated codes", err)
	}
	var actual []licenseFacts
	if err = target.DB.NewRaw(`SELECT id,device_limit,term_kind,term_amount,
COALESCE(to_char(term_end_date,'YYYY-MM-DD'),'') AS end_date,
COALESCE(to_char(term_starts_at AT TIME ZONE 'Asia/Shanghai','YYYY-MM-DD'),'') AS starts_at
FROM licenses WHERE fulfillment_mode='offline_file' ORDER BY id`).Scan(ctx, &actual); err != nil {
		t.Fatal("read imported license facts", err)
	}
	if len(actual) != len(expected) {
		t.Fatalf("imported license count = %d, want %d", len(actual), len(expected))
	}
	for _, facts := range actual {
		if want := expected[facts.ID]; facts != want {
			t.Fatalf("import changed unactivated license facts: got %+v, want %+v", facts, want)
		}
	}
	for id, want := range codes {
		got, codeErr := target.OfflineLicenseCode(ctx, targetActor, id)
		if codeErr != nil || got != want {
			t.Fatalf("import changed original unactivated license code: got=%q err=%v", got, codeErr)
		}
	}
}

func TestOfflineBusinessBundlePreservesFullyTransferredHistory(t *testing.T) {
	ctx := context.Background()
	svc, actor, _, base := offlineServiceFixture(t)
	var customerID string
	if err := svc.Store.DB.NewRaw("SELECT id FROM commercial_customers LIMIT 1").Scan(ctx, &customerID); err != nil {
		t.Fatal(err)
	}
	created, err := svc.Issue(ctx, actor, prepareOfflineCommand(t, svc, OfflineIntent{
		Action: "initial", CustomerID: customerID, DeviceLimit: 2, Term: offline.Term{Kind: "perpetual"},
	}))
	if err != nil {
		t.Fatal("create multi-device code", err)
	}
	identity, err := svc.Keys.Status()
	if err != nil {
		t.Fatal(err)
	}
	identity.State = "public"
	identityRaw, _ := json.Marshal(identity)
	if err = svc.Store.SetSetting(ctx, "snail_public_identity", string(identityRaw)); err != nil {
		t.Fatal(err)
	}
	client := &ClientActivationService{Store: svc.Store, Keys: svc.Keys, clock: func() time.Time { return base }}
	bindings := make([]string, 0, 2)
	for i, digest := range []string{"a", "b"} {
		result, activationErr := client.Activate(ctx, ClientActivationCommand{
			OperationID: store.NewID(), LicenseCode: created.LicenseCode, ProductID: offline.ProductID,
			Device:      offline.Device{Type: offline.MachineType, Digest: strings.Repeat(digest, 64)},
			DeviceLabel: "合成设备" + digest, AppVersion: "test", BuildID: "bundle-transfer-test",
		})
		if activationErr != nil {
			t.Fatal("activate multi-device code", i, activationErr)
		}
		bindings = append(bindings, result.BindingID)
	}
	var revision int64
	if err = svc.Store.DB.NewRaw("SELECT current_revision FROM licenses WHERE id=?", created.LicenseID).Scan(ctx, &revision); err != nil {
		t.Fatal("read activation revision", err)
	}
	for _, bindingID := range bindings {
		moved, moveErr := svc.Issue(ctx, actor, prepareOfflineCommand(t, svc, OfflineIntent{
			Action: "transfer", LicenseID: created.LicenseID, BindingID: bindingID, ExpectedRevision: revision, Reason: "合成释放设备",
		}))
		if moveErr != nil {
			t.Fatal("release multi-device binding", moveErr)
		}
		revision = moved.Revision
	}
	var active int
	if err = svc.Store.DB.NewRaw("SELECT count(*) FROM offline_device_bindings WHERE license_id=? AND superseded_at IS NULL", created.LicenseID).Scan(ctx, &active); err != nil || active != 0 {
		t.Fatal("test setup did not transfer every device", active, err)
	}
	bundleRaw, err := svc.Store.ExportBusinessBundle(ctx, identity)
	if err != nil {
		t.Fatal("export all-transferred license", err)
	}
	bundle, err := store.ParseBusinessBundle(bundleRaw)
	if err != nil {
		t.Fatal("parse all-transferred license", err)
	}
	codes := licenseCodesForBundle(t, ctx, svc.Store, actor, bundle)
	target, _, targetActor := blankBackupTarget(t)
	if _, err = target.ImportBusinessBundle(ctx, bundle, codes, targetActor, true); err != nil {
		t.Fatal("import all-transferred license", err)
	}
	var targetRevision, targetBindings, targetIssuances int64
	if err = target.DB.NewRaw("SELECT current_revision FROM licenses WHERE id=?", created.LicenseID).Scan(ctx, &targetRevision); err != nil {
		t.Fatal("read transferred target revision", err)
	}
	if err = target.DB.NewRaw("SELECT count(*) FROM offline_device_bindings WHERE license_id=? AND superseded_at IS NULL", created.LicenseID).Scan(ctx, &targetBindings); err != nil {
		t.Fatal("read transferred target bindings", err)
	}
	if err = target.DB.NewRaw("SELECT count(*) FROM license_issuances WHERE license_id=?", created.LicenseID).Scan(ctx, &targetIssuances); err != nil {
		t.Fatal("read transferred target history", err)
	}
	var sourceIssuances int64
	if err = svc.Store.DB.NewRaw("SELECT count(*) FROM license_issuances WHERE license_id=?", created.LicenseID).Scan(ctx, &sourceIssuances); err != nil {
		t.Fatal("read transferred source history", err)
	}
	if targetRevision != revision || targetBindings != 0 || targetIssuances != sourceIssuances {
		t.Fatalf("bundle lost transfer history: revision=%d bindings=%d issuances=%d", targetRevision, targetBindings, targetIssuances)
	}
	dsn, err := url.Parse(os.Getenv("SNAIL_LOCAL_TEST_DSN"))
	if err != nil {
		t.Fatal("parse isolated database URL", err)
	}
	var databaseName string
	if err = svc.Store.DB.NewRaw("SELECT current_database()").Scan(ctx, &databaseName); err != nil {
		t.Fatal("read source database name", err)
	}
	dsn.Path = "/" + databaseName
	backup := &OfflineBackupService{Store: svc.Store, Keys: svc.Keys, DatabaseURL: dsn.String(), DataDir: t.TempDir(), MigrationDir: "../../db/migrations", EncryptionSecret: strings.Repeat("07", 32)}
	fullBackup, err := backup.Backup(ctx, "Synthetic-all-transferred-backup", true, true)
	if err != nil {
		t.Fatal("backup all-transferred license", err)
	}
	candidate, err := backup.PreviewRestore(ctx, fullBackup, "Synthetic-all-transferred-backup")
	if err != nil {
		t.Fatal("preview all-transferred restore", err)
	}
	defer func() {
		if err := backup.DiscardRestore(ctx, candidate); err != nil {
			t.Error(err)
		}
	}()
	if err = backup.ValidateRestoreContinuity(ctx, candidate, ""); err != nil {
		t.Fatal("validate all-transferred restore", err)
	}
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
	if err = backup.ValidateRestoreContinuity(ctx, candidate, ""); err != nil {
		t.Fatal(err)
	}
	for revision := int64(1); revision <= 2; revision++ {
		intent := OfflineIntent{Action: "renew", LicenseID: first.LicenseID, ExpectedRevision: revision, Term: offline.Term{Kind: "days", Amount: 7}}
		if _, err = svc.Issue(ctx, actor, prepareOfflineCommand(t, svc, intent)); err != nil {
			t.Fatal(err)
		}
		if err = backup.ValidateRestoreContinuity(ctx, candidate, ""); err == nil {
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
	if err = backup.ValidateRestoreContinuity(ctx, current, ""); err != nil {
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
func TestOfflineUnregisteredEmptyBackupAndBundleRemainUsable(t *testing.T) {
	ctx := context.Background()
	s, keys, actor := blankBackupTarget(t)
	u, _ := url.Parse(os.Getenv("SNAIL_LOCAL_TEST_DSN"))
	var name string
	if err := s.DB.NewRaw("SELECT current_database()").Scan(ctx, &name); err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	backup := &OfflineBackupService{Store: s, Keys: keys, DatabaseURL: u.String(), DataDir: t.TempDir(), MigrationDir: "../../db/migrations", EncryptionSecret: strings.Repeat("09", 32)}
	raw, err := backup.Backup(ctx, "Synthetic-empty-identity-backup", false, false)
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := backup.PreviewRestore(ctx, raw, "Synthetic-empty-identity-backup")
	if err != nil {
		t.Fatal("unregistered backup rejected", err)
	}
	defer func() {
		if err := backup.DiscardRestore(ctx, candidate); err != nil {
			t.Error(err)
		}
	}()
	if err = backup.ValidateRestoreContinuity(ctx, candidate, ""); err != nil {
		t.Fatal(err)
	}
	bundleRaw, err := backup.ExportBusiness(ctx, actor, "Synthetic-empty-business-password")
	if err != nil {
		t.Fatal(err)
	}
	transfer, err := backup.OpenBusiness(bundleRaw, "Synthetic-empty-business-password")
	if err != nil {
		t.Fatal(err)
	}
	target, targetKeys, targetActor := blankBackupTarget(t)
	if _, err = target.ImportBusinessBundle(ctx, transfer.Bundle, transfer.LicenseCodes, targetActor, true); err != nil {
		t.Fatal(err)
	}
	if expected, e := target.ExpectedSigningIdentity(ctx, offline.PublicIdentity{}); e != nil || expected.KeyID != "" {
		t.Fatal("empty registration became corrupt", e)
	}
	created, err := targetKeys.Create("Synthetic-first-key-after-import", time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if expected, e := target.ExpectedSigningIdentity(ctx, created); e != nil || expected.KeyID != created.KeyID {
		t.Fatal("first identity blocked", e)
	}
	if err = target.SetSetting(ctx, "snail_public_identity", "null"); err != nil {
		t.Fatal(err)
	}
	if _, err = target.ExpectedSigningIdentity(ctx, created); err == nil {
		t.Fatal("null identity record accepted as an empty registration")
	}
}
func TestOfflineRestoreRepairsCorruptActiveContainerFromRegisteredIdentity(t *testing.T) {
	for _, missing := range []bool{false, true} {
		name := "corrupt"
		if missing {
			name = "missing"
		}
		t.Run(name, func(t *testing.T) { testOfflineRestoreUnavailableActiveIdentity(t, missing) })
	}
}
func testOfflineRestoreUnavailableActiveIdentity(t *testing.T, missing bool) {
	ctx := context.Background()
	backup, svc, actor, first, base := sourceBackupService(t)
	public, err := backup.Keys.Status()
	if err != nil {
		t.Fatal(err)
	}
	if err = backup.Store.SetSetting(ctx, "snail_public_identity", mustJSON(public)); err != nil {
		t.Fatal(err)
	}
	raw, err := backup.Backup(ctx, "Synthetic-corrupt-active-key", true, true)
	if err != nil {
		t.Fatal(err)
	}
	without, err := backup.Backup(ctx, "Synthetic-corrupt-active-key", false, false)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := backup.Keys.EncryptedIdentity()
	if err != nil {
		t.Fatal(err)
	}
	activeRoot := t.TempDir()
	active := offline.NewKeyStore(activeRoot)
	defer active.Lock()
	if err = active.Import(identity, public.KeyID); err != nil {
		t.Fatal(err)
	}
	backup.Keys = active
	activeFile := filepath.Join(activeRoot, "keys", "signing-identity.json")
	if missing {
		if err = os.Remove(activeFile); err != nil {
			t.Fatal(err)
		}
		if status, e := active.Status(); e != nil || status.State != "uncreated" {
			t.Fatal("fault did not remove active container", e)
		}
	} else {
		if err = os.WriteFile(activeFile, []byte("{"), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err = active.Status(); err == nil {
			t.Fatal("fault did not corrupt active container")
		}
	}
	for _, test := range []struct {
		raw   []byte
		allow bool
	}{{without, false}, {raw, true}} {
		candidate, e := backup.PreviewRestore(ctx, test.raw, "Synthetic-corrupt-active-key")
		if e != nil {
			t.Fatal(e)
		}
		if test.allow {
			required, ok := backup.ValidateRestoreContinuity(ctx, candidate, "").(*offline.Error)
			if !ok || required.Code != "RESTORE_SIGNING_PASSWORD_REQUIRED" {
				t.Fatal("damaged identity recovery did not require signing password")
			}
			if backup.ValidateRestoreContinuity(ctx, candidate, "Wrong-signing-password") == nil {
				t.Fatal("wrong signing password accepted")
			}
		}
		e = backup.ValidateRestoreContinuity(ctx, candidate, "Synthetic-service-signing-password")
		if test.allow {
			if e != nil {
				t.Fatal("healthy same-identity backup blocked", e)
			}
			restoredKeys := offline.NewKeyStore(candidate.IdentityRoot)
			if _, e = restoredKeys.Unlock("Synthetic-service-signing-password", time.Now().Add(time.Hour)); e != nil {
				t.Fatal(e)
			}
			restored := &OfflineIssuanceService{Store: candidate.Store, Keys: restoredKeys, PreviewSecret: bytes.Repeat([]byte{5}, 32), clock: func() time.Time { return base }}
			next, e := restored.Issue(ctx, actor, prepareOfflineCommand(t, restored, OfflineIntent{Action: "renew", LicenseID: first.LicenseID, ExpectedRevision: 1, Term: offline.Term{Kind: "days", Amount: 7}}))
			restoredKeys.Lock()
			if e != nil || next.Revision != 2 {
				t.Fatal("recovery did not preserve signing sequence", e)
			}
		} else if (e == nil) != missing {
			t.Fatal("identity-free restore changed the unavailable-key policy", e)
		}
		if e = backup.DiscardRestore(ctx, candidate); e != nil {
			t.Fatal(e)
		}
	}
	plain, err := offline.OpenBackup(raw, "Synthetic-corrupt-active-key")
	if err != nil {
		t.Fatal(err)
	}
	var payload FullBackupPayload
	if err = json.Unmarshal(plain, &payload); err != nil {
		t.Fatal(err)
	}
	clear(plain)
	container, err := offline.ParseContainer(payload.IdentityFile)
	if err != nil {
		t.Fatal(err)
	}
	firstByte := "A"
	if container.CiphertextB64[0] == 'A' {
		firstByte = "B"
	}
	container.CiphertextB64 = firstByte + container.CiphertextB64[1:]
	payload.IdentityFile, _ = json.Marshal(container)
	payload.IdentitySHA256 = digest(payload.IdentityFile)
	plain, err = json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	corrupt, err := offline.SealBackup(plain, "Synthetic-corrupt-active-key")
	clear(plain)
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := backup.PreviewRestore(ctx, corrupt, "Synthetic-corrupt-active-key")
	if err != nil {
		t.Fatal("structural corruption was not isolated to ciphertext", err)
	}
	if backup.ValidateRestoreContinuity(ctx, candidate, "Synthetic-service-signing-password") == nil {
		t.Fatal("unusable replacement ciphertext accepted")
	}
	if err = backup.DiscardRestore(ctx, candidate); err != nil {
		t.Fatal(err)
	}
	if countOfflineRows(t, svc.Store, "license_issuances") != 1 {
		t.Fatal("source issuance history changed")
	}
}
func TestOfflineBusinessTransferPreservesOriginalsAndRenews(t *testing.T) {
	ctx := context.Background()
	backup, source, actor, first, base := sourceBackupService(t)
	sourceCode, err := source.Store.OfflineLicenseCode(ctx, actor, first.LicenseID)
	if err != nil {
		t.Fatal("read source protected activation code", err)
	}
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
	const password = "Synthetic-business-transfer-password"
	raw, err := backup.ExportBusiness(ctx, actor, password)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"password_hash", "refresh_tokens", "license_key_encrypted", "jwtSecret", "encryptionSecret"} {
		if bytes.Contains(raw, []byte(`"`+forbidden+`"`)) {
			t.Fatalf("business export contains %s", forbidden)
		}
	}
	if bytes.Contains(raw, []byte(sourceCode)) {
		t.Fatal("business export exposed the original activation code")
	}
	transfer, err := backup.OpenBusiness(raw, password)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = backup.OpenBusiness(raw, "Synthetic-wrong-business-password"); err == nil {
		t.Fatal("business export opened with an incorrect password")
	}
	bundle := transfer.Bundle
	target, keys, targetActor := blankBackupTarget(t)
	summary, err := target.ImportBusinessBundle(ctx, bundle, transfer.LicenseCodes, targetActor, false)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Customers != 1 || summary.Licenses != 1 || summary.Originals != 1 || len(summary.Mappings) != 2 {
		t.Fatal("preview counts/mapping", summary)
	}
	if countOfflineRows(t, target, "commercial_customers") != 0 || countOfflineRows(t, target, "license_issuances") != 0 {
		t.Fatal("preview wrote business data")
	}
	if _, err = target.ImportBusinessBundle(ctx, bundle, transfer.LicenseCodes, targetActor, true); err != nil {
		t.Fatal(err)
	}
	importedCode, err := target.OfflineLicenseCode(ctx, targetActor, first.LicenseID)
	if err != nil || importedCode != sourceCode {
		t.Fatalf("business import changed original activation code: got=%q err=%v", importedCode, err)
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
	if _, err = target.ImportBusinessBundle(ctx, bundle, transfer.LicenseCodes, targetActor, true); err == nil {
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
			opened, err := backup.OpenBusiness(raw, password)
			if err != nil {
				t.Fatal(err)
			}
			b := opened.Bundle
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
			if _, err = fresh.ImportBusinessBundle(ctx, b, opened.LicenseCodes, owner, true); err == nil {
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
	// These opaque signed bytes are unchanged TEST_ONLY fixtures from the
	// 2026-10-23 schema baseline. Current parsers intentionally do not accept
	// their MachineGuid protocol.
	legacyRequest, err := os.ReadFile(filepath.Join("testdata", "source-07499ee-history", "TEST_ONLY.request-v2.snailreq"))
	if err != nil {
		t.Fatal(err)
	}
	legacyLicense, err := os.ReadFile(filepath.Join("testdata", "source-07499ee-history", "TEST_ONLY.license-v2.snaillic"))
	if err != nil {
		t.Fatal(err)
	}
	requestHash := sha256.Sum256(legacyRequest)
	licenseHash := sha256.Sum256(legacyLicense)
	if hex.EncodeToString(requestHash[:]) != "7ced1e9cbaed80ae0237fa658be64afe32c2ae3967e7f4ecef47d45d6653529d" || hex.EncodeToString(licenseHash[:]) != "f00cf2cac79761b04c6ce0df466cd54d7f0826662dd4db8cf47a81b173be05e1" {
		t.Fatal("historical fixture bytes changed")
	}
	var request struct {
		RequestID string `json:"requestId"`
		ProductID string `json:"productId"`
		Device    struct {
			Type   string `json:"type"`
			Digest string `json:"digest"`
		} `json:"device"`
		DeviceLabel string `json:"deviceLabel"`
		AppVersion  string `json:"appVersion"`
	}
	if err = json.Unmarshal(legacyRequest, &request); err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		PayloadB64 string `json:"payloadB64"`
	}
	if err = json.Unmarshal(legacyLicense, &envelope); err != nil {
		t.Fatal(err)
	}
	payloadRaw, err := base64.RawURLEncoding.Strict().DecodeString(envelope.PayloadB64)
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		LicenseID  string `json:"licenseId"`
		IssuanceID string `json:"issuanceId"`
		CustomerID string `json:"customerId"`
		Revision   int64  `json:"revision"`
		IssuedAt   string `json:"issuedAt"`
		NotBefore  string `json:"notBefore"`
		ExpiresAt  string `json:"expiresAt"`
		KeyID      string `json:"keyId"`
		Binding    struct {
			Type   string `json:"type"`
			Digest string `json:"digest"`
		} `json:"binding"`
	}
	if err = json.Unmarshal(payloadRaw, &payload); err != nil {
		t.Fatal(err)
	}
	readText := func(row store.BundleRow, column string) string {
		var value string
		_ = json.Unmarshal(row[column], &value)
		return value
	}
	setValue := func(row store.BundleRow, column string, value any) {
		raw, marshalErr := json.Marshal(value)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		row[column] = raw
	}
	fixtureTime := func(value string) string {
		parsed, parseErr := time.Parse("2006-01-02T15:04:05.000Z", value)
		if parseErr != nil {
			t.Fatal(parseErr)
		}
		return parsed.UTC().Format("2006-01-02T15:04:05-07:00")
	}
	findRow := func(table, column, value string) store.BundleRow {
		t.Helper()
		for _, row := range snapshot.Tables[table] {
			if readText(row, column) == value {
				return row
			}
		}
		t.Fatalf("snapshot %s row %s=%q not found", table, column, value)
		return nil
	}
	licenseRow := findRow("licenses", "id", first.LicenseID)
	customerID := readText(licenseRow, "customer_id")
	issuanceRow := findRow("license_issuances", "id", first.IssuanceID)
	requestID := readText(issuanceRow, "request_id")
	requestRow := findRow("offline_requests", "request_id", requestID)
	bindingID := readText(issuanceRow, "binding_id")
	bindingRow := findRow("offline_device_bindings", "id", bindingID)
	setValue(findRow("commercial_customers", "id", customerID), "id", payload.CustomerID)
	setValue(licenseRow, "id", payload.LicenseID)
	setValue(licenseRow, "customer_id", payload.CustomerID)
	setValue(licenseRow, "current_revision", payload.Revision)
	setValue(licenseRow, "current_issuance_id", payload.IssuanceID)
	setValue(licenseRow, "valid_from", fixtureTime(payload.NotBefore))
	setValue(licenseRow, "valid_until", fixtureTime(payload.ExpiresAt))
	setValue(licenseRow, "created_at", fixtureTime(payload.IssuedAt))
	setValue(licenseRow, "updated_at", fixtureTime(payload.IssuedAt))
	setValue(bindingRow, "license_id", payload.LicenseID)
	setValue(bindingRow, "device_type", payload.Binding.Type)
	setValue(bindingRow, "device_digest", payload.Binding.Digest)
	setValue(bindingRow, "device_label", request.DeviceLabel)
	setValue(bindingRow, "registered_at", fixtureTime(payload.IssuedAt))
	setValue(requestRow, "request_id", request.RequestID)
	setValue(requestRow, "product_code", request.ProductID)
	setValue(requestRow, "device_type", request.Device.Type)
	setValue(requestRow, "device_digest", request.Device.Digest)
	setValue(requestRow, "device_label", request.DeviceLabel)
	setValue(requestRow, "raw_bytes", `\x`+hex.EncodeToString(legacyRequest))
	setValue(requestRow, "created_at", fixtureTime(payload.IssuedAt))
	// This is ParseRequest's historical canonical JSON fingerprint from the
	// source baseline, not a value produced by the current SMBIOS parser.
	setValue(requestRow, "fingerprint", "0d93051629a8b9973db3bff33722d116c94cc92d22192a872c85f4cb95576ac4")
	setValue(issuanceRow, "id", payload.IssuanceID)
	setValue(issuanceRow, "license_id", payload.LicenseID)
	setValue(issuanceRow, "revision", payload.Revision)
	setValue(issuanceRow, "request_id", request.RequestID)
	setValue(issuanceRow, "key_id", payload.KeyID)
	setValue(issuanceRow, "not_before", fixtureTime(payload.NotBefore))
	setValue(issuanceRow, "expires_at", fixtureTime(payload.ExpiresAt))
	setValue(issuanceRow, "issued_at", fixtureTime(payload.IssuedAt))
	setValue(issuanceRow, "created_at", fixtureTime(payload.IssuedAt))
	setValue(issuanceRow, "raw_bytes", `\x`+hex.EncodeToString(legacyLicense))
	setValue(issuanceRow, "sha256", hex.EncodeToString(licenseHash[:]))
	for _, audit := range snapshot.Tables["audit_logs"] {
		if readText(audit, "entity") == "offline-license" && readText(audit, "entity_id") == first.LicenseID {
			setValue(audit, "entity_id", payload.LicenseID)
		}
	}
	var sourceLicense model.License
	if err = source.Store.DB.NewSelect().Model(&sourceLicense).Where("id=?", first.LicenseID).Scan(ctx); err != nil {
		t.Fatal(err)
	}
	plainCode := source.Store.DecryptLicenseKey(&sourceLicense)
	if plainCode == "" || source.Store.LicenseKeyAEAD == nil {
		t.Fatal("test license key unavailable for fixture remapping")
	}
	encryptedCode, err := source.Store.LicenseKeyAEAD.Encrypt([]byte(plainCode), []byte(payload.LicenseID))
	if err != nil {
		t.Fatal(err)
	}
	setValue(licenseRow, "license_key_encrypted", `\x`+hex.EncodeToString(encryptedCode))
	// Use the actual 2026-10-23 schema: drop fields introduced by later migrations
	// based on that isolated database's installed column catalog.
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
	var oldColumns []struct {
		Table  string `bun:"table_name"`
		Column string `bun:"column_name"`
	}
	if err = previous.DB.NewRaw("SELECT table_name,column_name FROM information_schema.columns WHERE table_schema='public'").Scan(ctx, &oldColumns); err != nil {
		t.Fatal(err)
	}
	allowed := map[string]map[string]bool{}
	for _, column := range oldColumns {
		if allowed[column.Table] == nil {
			allowed[column.Table] = map[string]bool{}
		}
		allowed[column.Table][column.Column] = true
	}
	for table, rows := range snapshot.Tables {
		for _, row := range rows {
			for column := range row {
				if !allowed[table][column] {
					delete(row, column)
				}
			}
		}
	}
	if err = previous.RestoreSnapshot(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	before, err := previous.LicenseIssuance(ctx, payload.IssuanceID)
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
	after, err := previous.LicenseIssuance(ctx, payload.IssuanceID)
	if err != nil || !bytes.Equal(before.RawBytes, after.RawBytes) || before.SHA256 != after.SHA256 || before.CreatedBy != after.CreatedBy {
		t.Fatal("upgrade changed signed original or provenance", err)
	}
	if !bytes.Equal(after.RawBytes, legacyLicense) || after.SHA256 != hex.EncodeToString(licenseHash[:]) || after.CreatedBy != actor {
		t.Fatal("upgrade did not retain the exact historical signed original and creator")
	}
	var restoredRequest model.OfflineRequest
	if err = previous.DB.NewSelect().Model(&restoredRequest).Where("request_id=?", request.RequestID).Scan(ctx); err != nil || !bytes.Equal(restoredRequest.RawBytes, legacyRequest) {
		t.Fatal("upgrade changed the historical request original", err)
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
