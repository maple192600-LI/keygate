package service

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"github.com/tabloy/keygate/internal/auth"
	keycrypto "github.com/tabloy/keygate/internal/crypto"
	"github.com/tabloy/keygate/internal/model"
	"github.com/tabloy/keygate/internal/offline"
	"github.com/tabloy/keygate/internal/store"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// All writes use a newly created database in the explicitly selected test cluster.
func offlineServiceFixture(t *testing.T) (*OfflineIssuanceService, string, OfflineIntent, time.Time) {
	t.Helper()
	dsn := os.Getenv("SNAIL_LOCAL_TEST_DSN")
	if dsn == "" {
		t.Skip("task-owned PostgreSQL DSN required")
	}
	ctx := context.Background()
	parent, err := store.New(dsn)
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	name := "snail_issuance_" + strings.ReplaceAll(store.NewID(), "-", "")
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
	if err = s.RunMigrations("../../db/migrations"); err != nil {
		t.Fatal(err)
	}
	s.LicenseKeyAEAD = keycrypto.MustDeriveAEAD(bytes.Repeat([]byte{7}, 32), "license-key")
	password, err := auth.HashPassword("Synthetic-service-owner-password")
	if err != nil {
		t.Fatal(err)
	}
	owner, err := s.InitializeLocal(ctx, "service_owner", password, "合成事务厂商", "合成联系方式")
	if err != nil {
		t.Fatal(err)
	}
	customer := &model.CommercialCustomer{ID: store.NewID(), DisplayName: "合成事务客户", CustomerType: "company", ContactMethod: "合成联系号", IssuanceEnabled: true}
	if err = s.SaveCommercialCustomer(ctx, customer, owner.ID, true); err != nil {
		t.Fatal(err)
	}
	request := offline.Request{FormatVersion: 2, RequestID: store.NewID(), ProductID: offline.ProductID, Device: offline.Device{Type: offline.MachineType, Digest: strings.Repeat("a", 64)}, DeviceLabel: "合成设备A", AppVersion: "test"}
	raw, _ := json.Marshal(request)
	_, fingerprint, err := offline.ParseRequest(raw, offline.ProductID)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.ImportOfflineRequest(ctx, &model.OfflineRequest{RequestID: request.RequestID, ProductCode: request.ProductID, DeviceType: request.Device.Type, DeviceDigest: request.Device.Digest, DeviceLabel: request.DeviceLabel, RawBytes: raw, Fingerprint: fingerprint, CreatedBy: owner.ID}); err != nil {
		t.Fatal(err)
	}
	keys := offline.NewKeyStore(t.TempDir())
	if _, err = keys.Create("Synthetic-service-signing-password", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(keys.Lock)
	base := time.Now().UTC().Truncate(time.Second)
	svc := &OfflineIssuanceService{Store: s, Keys: keys, PreviewSecret: bytes.Repeat([]byte{8}, 32), clock: func() time.Time { return base }}
	return svc, owner.ID, OfflineIntent{Action: "initial", CustomerID: customer.ID, RequestID: request.RequestID, Term: offline.Term{Kind: "days", Amount: 7}}, base
}
func prepareOfflineCommand(t *testing.T, s *OfflineIssuanceService, intent OfflineIntent) OfflineIssueCommand {
	t.Helper()
	p, err := s.Preview(context.Background(), intent)
	if err != nil {
		t.Fatal(err)
	}
	return OfflineIssueCommand{OfflineIntent: intent, OperationID: store.NewID(), PreviewBase: p.PreviewBase}
}

func TestOfflineRenewalAdvancesOriginalLicense(t *testing.T) {
	s, actor, intent, _ := offlineServiceFixture(t)
	ctx := context.Background()
	first, err := s.Issue(ctx, actor, prepareOfflineCommand(t, s, intent))
	if err != nil {
		t.Fatal(err)
	}
	renewal := OfflineIntent{Action: "renew", LicenseID: first.LicenseID, ExpectedRevision: first.Revision, Term: offline.Term{Kind: "days", Amount: 7}}
	renewed, err := s.Issue(ctx, actor, prepareOfflineCommand(t, s, renewal))
	if err != nil {
		t.Fatal(err)
	}
	if renewed.LicenseID != first.LicenseID || renewed.Revision != first.Revision+1 || len(renewed.Issuances) != 1 || renewed.Issuances[0].BindingID == "" {
		t.Fatalf("renewal did not advance the original license: first=%+v renewed=%+v", first, renewed)
	}
	previous, err := s.Store.LicenseIssuance(ctx, first.IssuanceID)
	if err != nil {
		t.Fatal(err)
	}
	current, err := s.Store.LicenseIssuance(ctx, renewed.IssuanceID)
	if err != nil {
		t.Fatal(err)
	}
	if current.LicenseID != previous.LicenseID || current.BindingID != previous.BindingID || current.PreviousIssuanceID != previous.ID || current.Revision != previous.Revision+1 {
		t.Fatalf("renewal did not preserve license and binding history: previous=%+v current=%+v", previous, current)
	}
}

func countOfflineRows(t *testing.T, s *store.Store, table string) int {
	t.Helper()
	var count int
	if err := s.DB.NewRaw("SELECT count(*) FROM "+table).Scan(context.Background(), &count); err != nil {
		t.Fatal(err)
	}
	return count
}
func TestOfflineIssuanceRollbackAndDurableIdempotency(t *testing.T) {
	for _, stage := range []string{"locked", "signed", "archived", "audited"} {
		t.Run(stage, func(t *testing.T) {
			s, actor, intent, base := offlineServiceFixture(t)
			ctx := context.Background()
			command := prepareOfflineCommand(t, s, intent)
			injected := errors.New("synthetic transaction failure")
			if stage == "locked" {
				s.Keys.Lock()
			} else {
				s.fault = func(at string) error {
					if at == stage {
						return injected
					}
					return nil
				}
			}
			if _, err := s.Issue(ctx, actor, command); err == nil {
				t.Fatal("fault must fail")
			}
			for _, table := range []string{"licenses", "license_issuances", "offline_device_bindings"} {
				if countOfflineRows(t, s.Store, table) != 0 {
					t.Fatalf("half committed %s", table)
				}
			}
			var auditCount int
			if err := s.Store.DB.NewRaw("SELECT count(*) FROM audit_logs WHERE entity='offline-license'").Scan(ctx, &auditCount); err != nil || auditCount != 0 {
				t.Fatal("audit or transaction leaked", err)
			}
			s.fault = nil
			if stage == "locked" {
				if _, err := s.Keys.Unlock("Synthetic-service-signing-password", time.Now().Add(time.Hour)); err != nil {
					t.Fatal(err)
				}
			}
			first, err := s.Issue(ctx, actor, command)
			if err != nil {
				t.Fatal(err)
			}
			original, err := s.Store.LicenseIssuance(ctx, first.IssuanceID)
			if err != nil {
				t.Fatal(err)
			}
			s.Keys.Lock()
			s.clock = func() time.Time { return base.Add(25 * time.Hour) }
			retry, err := s.Issue(ctx, actor, command)
			if err != nil || !retry.Replayed || retry.IssuanceID != first.IssuanceID || retry.SHA256 != first.SHA256 || retry.Revision != 1 {
				t.Fatal("25-hour exact retry differs", err, retry)
			}
			replayed, err := s.Store.LicenseIssuance(ctx, retry.IssuanceID)
			if err != nil || !bytes.Equal(original.RawBytes, replayed.RawBytes) {
				t.Fatal("original changed", err)
			}
			changed := command
			changed.Term.Amount = 8
			if _, err = s.Issue(ctx, actor, changed); err == nil {
				t.Fatal("same operation different intent accepted")
			}
			if countOfflineRows(t, s.Store, "licenses") != 1 || countOfflineRows(t, s.Store, "license_issuances") != 1 {
				t.Fatal("retry duplicated records")
			}
		})
	}
}
func TestOfflineConcurrentRenewalAndTransfer(t *testing.T) {
	s, actor, intent, base := offlineServiceFixture(t)
	ctx := context.Background()
	first, err := s.Issue(ctx, actor, prepareOfflineCommand(t, s, intent))
	if err != nil {
		t.Fatal(err)
	}
	original, err := s.Store.LicenseIssuance(ctx, first.IssuanceID)
	if err != nil {
		t.Fatal(err)
	}
	renew := OfflineIntent{Action: "renew", LicenseID: first.LicenseID, ExpectedRevision: 1, Term: offline.Term{Kind: "days", Amount: 7}}
	commands := []OfflineIssueCommand{prepareOfflineCommand(t, s, renew), prepareOfflineCommand(t, s, renew)}
	var wg sync.WaitGroup
	var mu sync.Mutex
	success, conflict := 0, 0
	var winner OfflineIssueResult
	for _, command := range commands {
		wg.Add(1)
		go func(command OfflineIssueCommand) {
			defer wg.Done()
			result, err := s.Issue(ctx, actor, command)
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				success++
				winner = result
			} else {
				var business *offline.Error
				if errors.As(err, &business) && business.Code == "REVISION_CONFLICT" {
					conflict++
				} else {
					t.Error(err)
				}
			}
		}(command)
	}
	wg.Wait()
	if success != 1 || conflict != 1 || winner.Revision != 2 {
		t.Fatal("concurrent renewal did not serialize", success, conflict)
	}
	if winner.ExpiresAt == nil || *winner.ExpiresAt != base.Add(14*24*time.Hour).Format("2006-01-02T15:04:05.000Z") || winner.NotBefore != base.Format("2006-01-02T15:04:05.000Z") {
		t.Fatal("renewal independently expected max(old,now)+7 days", winner)
	}
	transfer := OfflineIntent{Action: "transfer", LicenseID: first.LicenseID, BindingID: original.BindingID, ExpectedRevision: 2, Reason: "合成更换电脑"}
	moved, err := s.Issue(ctx, actor, prepareOfflineCommand(t, s, transfer))
	if err != nil {
		t.Fatal("release binding", err)
	}
	if moved.Revision != 3 || moved.ActiveDevices != 0 || moved.ExpiresAt == nil || *moved.ExpiresAt != *winner.ExpiresAt {
		t.Fatal("transfer changed expiry", moved)
	}
	old, err := s.Store.LicenseIssuance(ctx, first.IssuanceID)
	if err != nil || !bytes.Equal(old.RawBytes, original.RawBytes) {
		t.Fatal("old original was changed", err)
	}
	if countOfflineRows(t, s.Store, "offline_device_bindings") != 1 {
		t.Fatal("release created a second device binding")
	}
	var superseded bool
	if err = s.Store.DB.NewRaw("SELECT superseded_at IS NOT NULL FROM offline_device_bindings WHERE id=?", original.BindingID).Scan(ctx, &superseded); err != nil || !superseded {
		t.Fatal("old binding was not released", err)
	}
	var transferred model.LicenseIssuance
	if err = s.Store.DB.NewSelect().Model(&transferred).Where("license_id=? AND binding_id=? AND revision=?", first.LicenseID, original.BindingID, moved.Revision).Scan(ctx); err != nil {
		t.Fatal("transferred issuance was not archived", err)
	}
	identity, err := s.Keys.Status()
	if err != nil {
		t.Fatal(err)
	}
	public, err := offline.ParsePublic([]byte(identity.PublicKeyPEM))
	if err != nil {
		t.Fatal(err)
	}
	payload, err := offline.Verify(transferred.RawBytes, map[string]ed25519.PublicKey{identity.KeyID: public}, offline.ProductID)
	if err != nil || payload.BindingState != "transferred" || payload.Revision != moved.Revision || payload.Binding.Digest != strings.Repeat("a", 64) || payload.ExpiresAt == nil || *payload.ExpiresAt != *winner.ExpiresAt {
		t.Fatal("transferred issuance changed signed device or term", payload, err)
	}
}

func TestClientSyncReturnsSignedTransferAndReplacementActivationKeepsTerm(t *testing.T) {
	for _, initialTerm := range []offline.Term{{Kind: "days", Amount: 7}, {Kind: "perpetual"}} {
		t.Run(initialTerm.Kind, func(t *testing.T) {
			s, actor, _, base := offlineServiceFixture(t)
			ctx := context.Background()
			var customerID string
			if err := s.Store.DB.NewRaw("SELECT id FROM commercial_customers LIMIT 1").Scan(ctx, &customerID); err != nil {
				t.Fatal(err)
			}
			createCommand := prepareOfflineCommand(t, s, OfflineIntent{Action: "initial", CustomerID: customerID, DeviceLimit: 2, Term: initialTerm})
			created, err := s.Issue(ctx, actor, createCommand)
			if err != nil || created.LicenseCode == "" {
				t.Fatal("create license code", err)
			}
			createdAgain, err := s.Issue(ctx, actor, createCommand)
			if err != nil || !createdAgain.Replayed || createdAgain.LicenseID != created.LicenseID || createdAgain.LicenseCode != created.LicenseCode || countOfflineRows(t, s.Store, "licenses") != 1 {
				t.Fatal("license-code creation retry was not idempotent", createdAgain, err)
			}
			identity, err := s.Keys.Status()
			if err != nil {
				t.Fatal(err)
			}
			identity.State = "public"
			identityRaw, _ := json.Marshal(identity)
			if err = s.Store.SetSetting(ctx, "snail_public_identity", string(identityRaw)); err != nil {
				t.Fatal("register signing identity", err)
			}
			public, err := offline.ParsePublic([]byte(identity.PublicKeyPEM))
			if err != nil {
				t.Fatal(err)
			}
			trusted := map[string]ed25519.PublicKey{identity.KeyID: public}
			client := &ClientActivationService{Store: s.Store, Keys: s.Keys, clock: func() time.Time { return base }}
			activationCommand := func(digest, operationID string) ClientActivationCommand {
				t.Helper()
				return ClientActivationCommand{
					OperationID: operationID, LicenseCode: created.LicenseCode, ProductID: offline.ProductID,
					Device:      offline.Device{Type: offline.MachineType, Digest: strings.Repeat(digest, 64)},
					DeviceLabel: "合成设备" + digest, AppVersion: "test", BuildID: "transfer-test",
				}
			}
			activate := func(digest string) ClientLicenseResult {
				t.Helper()
				result, activationErr := client.Activate(ctx, activationCommand(digest, store.NewID()))
				if activationErr != nil {
					t.Fatal("activate by original code", activationErr)
				}
				return result
			}
			firstCommand := activationCommand("a", store.NewID())
			firstDevice, err := client.Activate(ctx, firstCommand)
			if err != nil {
				t.Fatal("first device activation", err)
			}
			firstReplay, err := client.Activate(ctx, firstCommand)
			if err != nil || firstReplay.RawLicense != firstDevice.RawLicense || firstReplay.BindingID != firstDevice.BindingID {
				t.Fatal("same activation operation did not replay its signed result", firstReplay, err)
			}
			secondDevice := activate("b")
			firstPayload, err := offline.Verify([]byte(firstDevice.RawLicense), trusted, offline.ProductID)
			if err != nil || firstPayload.Revision != 1 || firstPayload.BindingState != "active" {
				t.Fatal("first active grant", firstPayload, err)
			}
			secondPayload, err := offline.Verify([]byte(secondDevice.RawLicense), trusted, offline.ProductID)
			if err != nil || secondPayload.Revision != 2 || secondPayload.BindingState != "active" || firstPayload.NotBefore != secondPayload.NotBefore || (firstPayload.ExpiresAt == nil) != (secondPayload.ExpiresAt == nil) || (firstPayload.ExpiresAt != nil && *firstPayload.ExpiresAt != *secondPayload.ExpiresAt) {
				t.Fatal("second activation changed signed term", secondPayload, err)
			}
			if initialTerm.Kind == "days" {
				renewal := OfflineIntent{Action: "renew", LicenseID: created.LicenseID, ExpectedRevision: 2, Term: offline.Term{Kind: "days", Amount: 7}}
				renewCommand := prepareOfflineCommand(t, s, renewal)
				renewed, renewErr := s.Issue(ctx, actor, renewCommand)
				if renewErr != nil || renewed.Revision != 3 || renewed.ExpiresAt == nil {
					t.Fatal("renew existing license", renewed, renewErr)
				}
				issuanceCount := countOfflineRows(t, s.Store, "license_issuances")
				renewedAgain, renewErr := s.Issue(ctx, actor, renewCommand)
				if renewErr != nil || !renewedAgain.Replayed || renewedAgain.Revision != renewed.Revision || renewedAgain.ExpiresAt == nil || *renewedAgain.ExpiresAt != *renewed.ExpiresAt || countOfflineRows(t, s.Store, "license_issuances") != issuanceCount {
					t.Fatal("same renewal operation changed revision or added an issuance", renewedAgain, renewErr)
				}
			}
			var expectedRevision int64
			if err = s.Store.DB.NewRaw("SELECT max(revision) FROM license_issuances WHERE license_id=? AND binding_id=?", created.LicenseID, firstDevice.BindingID).Scan(ctx, &expectedRevision); err != nil {
				t.Fatal(err)
			}
			var source model.LicenseIssuance
			if err = s.Store.DB.NewSelect().Model(&source).Where("license_id=? AND binding_id=? AND revision=?", created.LicenseID, firstDevice.BindingID, expectedRevision).Scan(ctx); err != nil {
				t.Fatal("latest source grant", err)
			}
			sourcePayload, err := offline.Verify(source.RawBytes, trusted, offline.ProductID)
			if err != nil || sourcePayload.BindingState != "active" {
				t.Fatal("source grant is not active", sourcePayload, err)
			}
			transferIntent := OfflineIntent{Action: "transfer", LicenseID: created.LicenseID, BindingID: firstDevice.BindingID, ExpectedRevision: expectedRevision, Reason: "合成更换电脑"}
			moved, err := s.Issue(ctx, actor, prepareOfflineCommand(t, s, transferIntent))
			if err != nil {
				t.Fatal("release selected binding", err)
			}
			if moved.Revision != expectedRevision+1 || moved.ActiveDevices != 1 || len(moved.Issuances) != 2 {
				t.Fatal("transfer did not archive both binding states", moved)
			}
			var activeSibling model.LicenseIssuance
			if err = s.Store.DB.NewSelect().Model(&activeSibling).Where("license_id=? AND binding_id=? AND revision=?", created.LicenseID, secondDevice.BindingID, moved.Revision).Scan(ctx); err != nil {
				t.Fatal("active sibling issuance", err)
			}
			activePayload, err := offline.Verify(activeSibling.RawBytes, trusted, offline.ProductID)
			if err != nil || activePayload.BindingState != "active" || activePayload.Revision != moved.Revision || activePayload.NotBefore != sourcePayload.NotBefore || (activePayload.ExpiresAt == nil) != (sourcePayload.ExpiresAt == nil) || (activePayload.ExpiresAt != nil && *activePayload.ExpiresAt != *sourcePayload.ExpiresAt) {
				t.Fatal("transfer changed active sibling grant", activePayload, err)
			}
			syncResult, err := client.Sync(ctx, ClientSyncCommand{RawLicense: string(source.RawBytes), Device: sourcePayload.Binding, DeviceLabel: "合成设备a", AppVersion: "test", BuildID: "transfer-test"})
			if err != nil {
				t.Fatal("sync after transfer", err)
			}
			transferredPayload, err := offline.Verify([]byte(syncResult.RawLicense), trusted, offline.ProductID)
			if err != nil || transferredPayload.BindingState != "transferred" || transferredPayload.Revision != moved.Revision || transferredPayload.IssuanceID == sourcePayload.IssuanceID || transferredPayload.LicenseID != sourcePayload.LicenseID || transferredPayload.CustomerID != sourcePayload.CustomerID || transferredPayload.Binding != sourcePayload.Binding || transferredPayload.NotBefore != sourcePayload.NotBefore || (transferredPayload.ExpiresAt == nil) != (sourcePayload.ExpiresAt == nil) || (transferredPayload.ExpiresAt != nil && *transferredPayload.ExpiresAt != *sourcePayload.ExpiresAt) {
				t.Fatal("sync did not return the signed transfer result", transferredPayload, err)
			}
			if syncResult.BindingID != firstDevice.BindingID || syncResult.LicenseID != created.LicenseID || syncResult.RawRecovery != "" {
				t.Fatal("transfer sync response identity", syncResult)
			}
			replacementCommand := activationCommand("c", store.NewID())
			s.Keys.Lock()
			if _, err = client.Activate(ctx, replacementCommand); err == nil {
				t.Fatal("activation succeeded while the signing key was locked")
			}
			var replacementRows int
			if err = s.Store.DB.NewRaw("SELECT count(*) FROM offline_device_bindings WHERE license_id=? AND device_digest=?", created.LicenseID, strings.Repeat("c", 64)).Scan(ctx, &replacementRows); err != nil || replacementRows != 0 {
				t.Fatal("failed activation left a device binding", replacementRows, err)
			}
			if _, err = s.Keys.Unlock("Synthetic-service-signing-password", time.Now().Add(time.Hour)); err != nil {
				t.Fatal("unlock signing key for exact retry", err)
			}
			replacement, err := client.Activate(ctx, replacementCommand)
			if err != nil {
				t.Fatal("retry activation after signing failure", err)
			}
			replacementPayload, err := offline.Verify([]byte(replacement.RawLicense), trusted, offline.ProductID)
			if err != nil || replacementPayload.BindingState != "active" || replacementPayload.Revision != moved.Revision+1 || replacementPayload.NotBefore != sourcePayload.NotBefore || (replacementPayload.ExpiresAt == nil) != (sourcePayload.ExpiresAt == nil) || (replacementPayload.ExpiresAt != nil && *replacementPayload.ExpiresAt != *sourcePayload.ExpiresAt) {
				t.Fatal("replacement activation changed the original term", replacementPayload, err)
			}
			replayedTransfer, err := client.Sync(ctx, ClientSyncCommand{RawLicense: syncResult.RawLicense, Device: transferredPayload.Binding})
			if err != nil || replayedTransfer.RawLicense != syncResult.RawLicense {
				t.Fatal("transferred sync did not remain stable after a later global revision", err)
			}
			if _, err = client.Activate(ctx, activationCommand("d", store.NewID())); err == nil {
				t.Fatal("device limit did not prevent a third current binding")
			} else {
				var business *offline.Error
				if !errors.As(err, &business) || business.Code != "LICENSE_DEVICE_LIMIT" {
					t.Fatal("third activation failed for an unrelated reason", err)
				}
			}
			var activeCount int
			if err = s.Store.DB.NewRaw("SELECT count(*) FROM offline_device_bindings WHERE license_id=? AND superseded_at IS NULL", created.LicenseID).Scan(ctx, &activeCount); err != nil || activeCount != 2 {
				t.Fatal("device-limit rejection changed active bindings", activeCount, err)
			}
			if outputDir := os.Getenv("KEYGATE_TRANSFER_FIXTURE_DIR"); outputDir != "" && initialTerm.Kind == "days" {
				if err = os.MkdirAll(outputDir, 0700); err != nil {
					t.Fatal("create fixture output directory", err)
				}
				for name, raw := range map[string]string{
					"source-active.snaillic":      string(source.RawBytes),
					"transferred.snaillic":        syncResult.RawLicense,
					"sibling-active.snaillic":     string(activeSibling.RawBytes),
					"replacement-active.snaillic": replacement.RawLicense,
				} {
					if err = os.WriteFile(filepath.Join(outputDir, name), []byte(raw), 0600); err != nil {
						t.Fatal("write Go-generated client fixture", err)
					}
				}
				if err = os.WriteFile(filepath.Join(outputDir, "public-key.pem"), []byte(identity.PublicKeyPEM), 0600); err != nil {
					t.Fatal("write Go-generated client fixture public key", err)
				}
			}
		})
	}
}

func TestTransferIntentRequiresSelectingAnExistingBinding(t *testing.T) {
	base := OfflineIntent{Action: "transfer", LicenseID: store.NewID(), ExpectedRevision: 1, Reason: "合成换机"}
	requestBased := base
	requestBased.RequestID = store.NewID()
	if _, err := cleanIntent(requestBased); err == nil {
		t.Fatal("request-file transfer path accepted")
	}
	selected := base
	selected.BindingID = store.NewID()
	if _, err := cleanIntent(selected); err != nil {
		t.Fatal("selected-binding release rejected", err)
	}
}
