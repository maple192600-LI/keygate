package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/tabloy/keygate/internal/auth"
	"github.com/tabloy/keygate/internal/model"
	"github.com/tabloy/keygate/internal/offline"
	"github.com/tabloy/keygate/internal/store"
	keycrypto "github.com/tabloy/keygate/internal/crypto"
	"net/url"
	"os"
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
	request := offline.Request{FormatVersion: 2, RequestID: store.NewID(), ProductID: offline.ProductID, Device: offline.Device{Type: offline.MachineType, Digest: strings.Repeat("b", 64)}, DeviceLabel: "合成新设备B"}
	raw, _ := json.Marshal(request)
	_, fingerprint, _ := offline.ParseRequest(raw, offline.ProductID)
	if _, _, err = s.Store.ImportOfflineRequest(ctx, &model.OfflineRequest{RequestID: request.RequestID, ProductCode: request.ProductID, DeviceType: request.Device.Type, DeviceDigest: request.Device.Digest, DeviceLabel: request.DeviceLabel, RawBytes: raw, Fingerprint: fingerprint, CreatedBy: actor}); err != nil {
		t.Fatal(err)
	}
	transfer := OfflineIntent{Action: "transfer", LicenseID: first.LicenseID, RequestID: request.RequestID, ExpectedRevision: 2, Reason: "合成更换电脑"}
	moved, err := s.Issue(ctx, actor, prepareOfflineCommand(t, s, transfer))
	if err != nil {
		t.Fatal(err)
	}
	if moved.Revision != 3 || moved.ExpiresAt == nil || *moved.ExpiresAt != *winner.ExpiresAt {
		t.Fatal("transfer changed expiry", moved)
	}
	old, err := s.Store.LicenseIssuance(ctx, first.IssuanceID)
	if err != nil || !bytes.Equal(old.RawBytes, original.RawBytes) {
		t.Fatal("old original was changed", err)
	}
	if countOfflineRows(t, s.Store, "offline_device_bindings") != 2 {
		t.Fatal("old binding history lost")
	}
	var currentDigest string
	if err = s.Store.DB.NewRaw("SELECT device_digest FROM offline_device_bindings WHERE license_id=? AND superseded_at IS NULL", first.LicenseID).Scan(ctx, &currentDigest); err != nil || currentDigest != strings.Repeat("b", 64) {
		t.Fatal("transfer binding", err)
	}
}
