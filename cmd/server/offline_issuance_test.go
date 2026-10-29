package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/tabloy/keygate/internal/model"
	"github.com/tabloy/keygate/internal/offline"
	"github.com/tabloy/keygate/internal/service"
	"github.com/tabloy/keygate/internal/store"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

func decodeLocal[T any](t *testing.T, body []byte) T {
	t.Helper()
	var envelope struct {
		Data T `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatal(err)
	}
	return envelope.Data
}
func TestLocalOfflineIssuanceHTTP(t *testing.T) {
	type identityStatus struct {
		Identity         offline.PublicIdentity `json:"identity"`
		AutomaticSigning bool                   `json:"automaticSigning"`
	}
	a := localApp(t)
	password := "Synthetic-issuing-owner-password"
	if status, body := a.call(t, "POST", "/api/v1/local/setup", map[string]string{"username": "issuing_owner", "password": password, "siteName": "签发隔离厂商", "contact": "合成联系方式"}); status != 201 {
		t.Fatalf("setup %d %s", status, body)
	}
	if status, _ := a.call(t, "POST", "/api/v1/auth/password/login", map[string]string{"username": "issuing_owner", "password": password}); status != 200 {
		t.Fatal("login")
	}
	status, body := a.call(t, "POST", "/api/v1/admin/commercial-customers", map[string]any{"displayName": "无邮箱签发客户", "customerType": "company", "contactMethod": "合成联系电话", "email": nil})
	if status != 201 {
		t.Fatalf("customer %d %s", status, body)
	}
	customer := decodeLocal[struct {
		ID string `json:"id"`
	}](t, body)
	status, body = a.call(t, "POST", "/api/v1/admin/offline-requests", map[string]string{"deviceCode": "STQ-WIN1-" + strings.Repeat("a", 64), "deviceLabel": "合成设备 A"})
	if status != 200 {
		t.Fatalf("request %d %s", status, body)
	}
	request := decodeLocal[struct {
		Request struct {
			ID string `json:"requestId"`
		} `json:"request"`
	}](t, body)
	intent := service.OfflineIntent{Action: "initial", CustomerID: customer.ID, RequestID: request.Request.ID, Term: offline.Term{Kind: "days", Amount: 7}}
	status, body = a.call(t, "POST", "/api/v1/admin/offline-licenses/preview", intent)
	if status != 200 {
		t.Fatalf("preview %d %s", status, body)
	}
	preview := decodeLocal[service.OfflinePreview](t, body)
	base, err := offline.ParseUTC(preview.BaseTime)
	if err != nil {
		t.Fatal(err)
	}
	expected := base.Add(7 * 24 * time.Hour).Format("2006-01-02T15:04:05.000Z")
	if preview.ExpiresAt == nil || *preview.ExpiresAt != expected || preview.NotBefore != preview.BaseTime {
		t.Fatal("preview differs from independent 7x24-hour expectation")
	}
	status, body = a.call(t, "GET", "/api/v1/admin/offline-key/public", nil)
	if status != 200 {
		t.Fatalf("automatic signing identity %d %s", status, body)
	}
	public := decodeLocal[identityStatus](t, body)
	identity := public.Identity
	if !public.AutomaticSigning || identity.KeyID != "" || identity.PublicKeyPEM != "" {
		t.Fatalf("fresh service created a signing identity before issuance: %+v", public)
	}
	command := service.OfflineIssueCommand{OfflineIntent: intent, OperationID: store.NewID(), PreviewBase: preview.PreviewBase}
	var wg sync.WaitGroup
	results := make(chan service.OfflineIssueResult, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			status, body := a.call(t, "POST", "/api/v1/admin/offline-licenses", command)
			if status != 200 {
				t.Errorf("issue %d %s", status, body)
				return
			}
			results <- decodeLocal[service.OfflineIssueResult](t, body)
		}()
	}
	wg.Wait()
	close(results)
	var result service.OfflineIssueResult
	replays := 0
	count := 0
	for row := range results {
		if count > 0 && (row.IssuanceID != result.IssuanceID || row.SHA256 != result.SHA256) {
			t.Fatal("concurrent operation created distinct originals")
		}
		result = row
		count++
		if row.Replayed {
			replays++
		}
	}
	if count != 2 || replays != 1 {
		t.Fatalf("count=%d replay=%d", count, replays)
	}
	status, body = a.call(t, "GET", "/api/v1/admin/offline-key/public", nil)
	if status != 200 {
		t.Fatalf("automatic signing identity after issuance %d %s", status, body)
	}
	public = decodeLocal[identityStatus](t, body)
	identity = public.Identity
	if !public.AutomaticSigning || identity.KeyID == "" || identity.PublicKeyPEM == "" {
		t.Fatalf("automatic signing identity was not established at issuance: %+v", public)
	}
	pub, err := offline.ParsePublic([]byte(identity.PublicKeyPEM))
	if err != nil {
		t.Fatal(err)
	}
	// Online lifecycle helpers must refuse even an otherwise qualifying
	// offline row; the original signed deadline and status stay unchanged.
	var beforeOnlineRow string
	if err = a.store.DB.NewRaw("SELECT row_to_json(l)::text FROM licenses l WHERE id=?", result.LicenseID).Scan(context.Background(), &beforeOnlineRow); err != nil {
		t.Fatal(err)
	}
	beforeOnline := time.Now().Add(365 * 24 * time.Hour)
	if changed, err := a.store.ExpireLicenseIf(context.Background(), result.LicenseID, []string{"active"}, &beforeOnline); err != nil || changed {
		t.Fatalf("online expiry touched offline file: %v %v", changed, err)
	}
	if err := a.store.SuspendLicense(context.Background(), result.LicenseID); err == nil {
		t.Fatal("online suspend accepted offline file")
	}
	if err := a.store.RevokeLicense(context.Background(), result.LicenseID); err == nil {
		t.Fatal("online revoke accepted offline file")
	}
	if _, _, err := a.store.SetLicenseValidUntil(context.Background(), result.LicenseID, &beforeOnline); err == nil {
		t.Fatal("online date edit accepted offline file")
	}
	if err := a.store.SyncSubscriptionStatuses(context.Background()); err != nil {
		t.Fatal(err)
	}
	var actorID, planID string
	if err = a.store.DB.NewRaw("SELECT id FROM users WHERE username='issuing_owner'").Scan(context.Background(), &actorID); err != nil {
		t.Fatal(err)
	}
	if err = a.store.DB.NewRaw("SELECT plan_id FROM licenses WHERE id=?", result.LicenseID).Scan(context.Background(), &planID); err != nil {
		t.Fatal(err)
	}
	if err = a.store.UpdateLicenseUser(context.Background(), result.LicenseID, actorID); err == nil {
		t.Fatal("online user binding accepted offline license")
	}
	if err = a.store.CreateActivation(context.Background(), &model.Activation{LicenseID: result.LicenseID, Identifier: "synthetic-online-activation"}); err == nil {
		t.Fatal("online activation accepted offline license")
	}
	if err = a.store.CreateSubscription(context.Background(), &model.Subscription{LicenseID: result.LicenseID, PlanID: planID, Status: "active"}); err == nil {
		t.Fatal("online subscription accepted offline license")
	}
	if _, _, err = a.store.IncrementUsageCounterWithLimit(context.Background(), result.LicenseID, "api_calls", "monthly", "2026-10", 1, 100); err == nil {
		t.Fatal("online usage write accepted offline license")
	}
	if _, err = a.store.ReinstateLicense(context.Background(), result.LicenseID); err == nil {
		t.Fatal("online reinstate accepted offline license")
	}
	a.store.UpdateLicenseEmailByStripeCustomer(context.Background(), "", "must-not-save@example.invalid")
	var afterOnlineRow string
	if err = a.store.DB.NewRaw("SELECT row_to_json(l)::text FROM licenses l WHERE id=?", result.LicenseID).Scan(context.Background(), &afterOnlineRow); err != nil || beforeOnlineRow != afterOnlineRow {
		t.Fatal("native writer changed offline metadata", err)
	}
	var actualStatus string
	var actualUntil time.Time
	if err := a.store.DB.NewRaw("SELECT status,valid_until FROM licenses WHERE id=?", result.LicenseID).Scan(context.Background(), &actualStatus, &actualUntil); err != nil {
		t.Fatal(err)
	}
	if actualStatus != "active" || actualUntil.UTC().Format("2006-01-02T15:04:05.000Z") != expected {
		t.Fatal("online lifecycle changed signed license metadata")
	}
	status, body, headers := a.callHeaders(t, "GET", "/api/v1/admin/offline-licenses/"+result.LicenseID+"/code", nil)
	code := decodeLocal[struct {
		LicenseCode string `json:"licenseCode"`
	}](t, body)
	if status != 200 || code.LicenseCode == "" || !strings.Contains(strings.ToLower(headers.Get("Cache-Control")), "no-store") {
		t.Fatalf("license code must be owner-readable and not cacheable: %d %s %v", status, body, headers)
	}
	status, file := a.call(t, "GET", "/api/v1/admin/license-issuances/"+url.PathEscape(result.IssuanceID)+"/file", nil)
	if status != 200 {
		t.Fatalf("download %d %s", status, file)
	}
	sum := sha256.Sum256(file)
	if hex.EncodeToString(sum[:]) != result.SHA256 {
		t.Fatal("archive hash differs")
	}
	payload, err := offline.Verify(file, map[string]ed25519.PublicKey{identity.KeyID: pub}, offline.ProductID)
	if err != nil || payload.CustomerID != customer.ID || payload.Binding.Digest != strings.Repeat("a", 64) || payload.Revision != 1 || payload.ExpiresAt == nil || *payload.ExpiresAt != expected {
		t.Fatalf("signed product result %v %+v", err, payload)
	}
	_, second := a.call(t, "GET", "/api/v1/admin/license-issuances/"+result.IssuanceID+"/file", nil)
	if !bytes.Equal(file, second) {
		t.Fatal("redownload changed original")
	}
	deliveryPath := "/api/v1/admin/license-issuances/" + result.IssuanceID + "/deliveries"
	if status, body := a.call(t, "GET", deliveryPath, nil); status != 200 || len(decodeLocal[struct {
		Items []any `json:"items"`
	}](t, body).Items) != 0 {
		t.Fatal("downloads must not mark delivery")
	}
	delivery := map[string]string{"operationId": store.NewID(), "method": "wechat", "recipient": "合成收件对象", "deliveredAt": "2026-10-22T16:00:00.000Z", "notes": "手工登记"}
	var deliveryReplays int
	for range 2 {
		status, body := a.call(t, "POST", deliveryPath, delivery)
		if status != 200 {
			t.Fatalf("delivery %d %s", status, body)
		}
		value := decodeLocal[struct {
			Replayed bool `json:"replayed"`
		}](t, body)
		if value.Replayed {
			deliveryReplays++
		}
	}
	if deliveryReplays != 1 {
		t.Fatal("delivery replay must reuse one row")
	}
	delivery["recipient"] = "同操作号不同对象"
	if status, _ := a.call(t, "POST", deliveryPath, delivery); status != 409 {
		t.Fatal("delivery operation conflict must refuse")
	}
	if status, body := a.call(t, "GET", deliveryPath, nil); status != 200 || len(decodeLocal[struct {
		Items []any `json:"items"`
	}](t, body).Items) != 1 {
		t.Fatal("delivery row count")
	}
	if status, body := a.call(t, "POST", "/api/v1/auth/logout", nil); status != 200 {
		t.Fatalf("logout %d %s", status, body)
	}
	if status, body := a.call(t, "POST", "/api/v1/auth/password/login", map[string]string{"username": "issuing_owner", "password": password}); status != 200 {
		t.Fatalf("login after logout %d %s", status, body)
	}
	status, signingBody := a.call(t, "GET", "/api/v1/admin/offline-key/public", nil)
	if status != 200 {
		t.Fatalf("signing status after logout %d %s", status, signingBody)
	}
	statusAfterLogout := decodeLocal[identityStatus](t, signingBody)
	if !statusAfterLogout.AutomaticSigning || statusAfterLogout.Identity.KeyID != identity.KeyID {
		t.Fatalf("owner logout stopped service signing: %+v response=%s", statusAfterLogout, signingBody)
	}
	renewIntent := service.OfflineIntent{Action: "renew", LicenseID: result.LicenseID, ExpectedRevision: 1, Term: offline.Term{Kind: "days", Amount: 7}}
	status, body = a.call(t, "POST", "/api/v1/admin/offline-licenses/preview", renewIntent)
	if status != 200 {
		t.Fatalf("renew preview after logout %d %s", status, body)
	}
	renewPreview := decodeLocal[service.OfflinePreview](t, body)
	renewCommand := service.OfflineIssueCommand{OfflineIntent: renewIntent, OperationID: store.NewID(), PreviewBase: renewPreview.PreviewBase}
	status, body = a.call(t, "POST", "/api/v1/admin/offline-licenses/"+result.LicenseID+"/renew", renewCommand)
	if status != 200 || decodeLocal[service.OfflineIssueResult](t, body).Revision != 2 {
		t.Fatalf("renew after logout %d %s", status, body)
	}
	conflicting := command
	conflicting.Term.Amount = 8
	if status, body := a.call(t, "POST", "/api/v1/admin/offline-licenses", conflicting); status != 409 || !bytes.Contains(body, []byte("OPERATION_CONFLICT")) {
		t.Fatalf("operation conflict %d %s", status, body)
	}
	if status, _ := a.call(t, "GET", "/api/v1/admin/issuance-operations/"+command.OperationID, nil); status != 200 {
		t.Fatal("operation recovery")
	}

	statePath := "/api/v1/admin/offline-licenses/" + result.LicenseID + "/issuance-state"
	stateCommand := map[string]any{"operationId": store.NewID(), "enabled": false, "expectedEnabled": true, "expectedRevision": 2, "reason": "合成停发检查"}
	if status, body := a.call(t, "POST", statePath, stateCommand); status != 200 {
		t.Fatalf("stop %d %s", status, body)
	}
	if status, body := a.call(t, "POST", statePath, stateCommand); status != 200 || !decodeLocal[struct {
		Replayed bool `json:"replayed"`
	}](t, body).Replayed {
		t.Fatal("state operation not idempotent", status, string(body))
	}
	renew := service.OfflineIntent{Action: "renew", LicenseID: result.LicenseID, ExpectedRevision: 2, Term: offline.Term{Kind: "days", Amount: 7}}
	if status, body := a.call(t, "POST", "/api/v1/admin/offline-licenses/preview", renew); status != 409 || !bytes.Contains(body, []byte("ISSUANCE_STOPPED")) {
		t.Fatal("stopped issuance still allowed", status, string(body))
	}
	if status, body := a.call(t, "GET", "/api/v1/admin/license-issuances/"+result.IssuanceID+"/file", nil); status != 200 || !bytes.Equal(body, file) {
		t.Fatal("stopping changed previously issued bytes")
	}
	status, body = a.call(t, "GET", "/api/v1/admin/offline-licenses?deliveryState=pending&termState=valid", nil)
	listing := decodeLocal[struct {
		Items []store.OfflineLicenseView `json:"items"`
		Total int                        `json:"total"`
	}](t, body)
	if status != 200 || listing.Total != 1 || len(listing.Items) != 1 || listing.Items[0].IssuanceEnabled || listing.Items[0].UsageState != "unreported" {
		t.Fatalf("independent lifecycle list %d %s", status, body)
	}
	status, body = a.call(t, "GET", "/api/v1/admin/offline-licenses/"+result.LicenseID, nil)
	if status != 200 || !bytes.Contains(body, []byte("deliveries")) || bytes.Contains(body, []byte("rawBytes")) {
		t.Fatalf("detail %d %s", status, body)
	}
	status, body = a.call(t, "GET", "/api/v1/admin/overview", nil)
	overview := decodeLocal[store.OfflineOverview](t, body)
	if status != 200 || overview.Customers != 1 || overview.Licenses != 1 || overview.Valid != 1 || overview.Stopped != 1 || overview.PendingDelivery != 1 || overview.UsageState != "unreported" {
		t.Fatalf("overview %d %s", status, body)
	}
	stateCommand = map[string]any{"operationId": store.NewID(), "enabled": true, "expectedEnabled": false, "expectedRevision": 2, "reason": "合成恢复续发检查"}
	if status, body := a.call(t, "POST", statePath, stateCommand); status != 200 {
		t.Fatalf("resume %d %s", status, body)
	}
}

func TestLocalUnactivatedLicenseCodeDeliveryHTTP(t *testing.T) {
	a := localApp(t)
	password := "Synthetic-code-delivery-owner-password"
	if status, body := a.call(t, "POST", "/api/v1/local/setup", map[string]string{"username": "delivery_owner", "password": password, "siteName": "合成交付厂商", "contact": "合成联系方式"}); status != 201 {
		t.Fatalf("setup %d %s", status, body)
	}
	if status, body := a.call(t, "POST", "/api/v1/auth/password/login", map[string]string{"username": "delivery_owner", "password": password}); status != 200 {
		t.Fatalf("login %d %s", status, body)
	}
	status, body := a.call(t, "POST", "/api/v1/admin/commercial-customers", map[string]any{"displayName": "合成授权码客户", "customerType": "company", "contactMethod": "合成联系号"})
	if status != 201 {
		t.Fatalf("customer %d %s", status, body)
	}
	customer := decodeLocal[struct {
		ID string `json:"id"`
	}](t, body)
	intent := service.OfflineIntent{Action: "initial", CustomerID: customer.ID, DeviceLimit: 1, Term: offline.Term{Kind: "perpetual"}}
	status, body = a.call(t, "POST", "/api/v1/admin/offline-licenses/preview", intent)
	if status != 200 {
		t.Fatalf("preview %d %s", status, body)
	}
	preview := decodeLocal[service.OfflinePreview](t, body)
	command := service.OfflineIssueCommand{OfflineIntent: intent, OperationID: store.NewID(), PreviewBase: preview.PreviewBase}
	status, body = a.call(t, "POST", "/api/v1/admin/offline-licenses", command)
	if status != 200 {
		t.Fatalf("create code %d %s", status, body)
	}
	created := decodeLocal[service.OfflineIssueResult](t, body)
	if created.LicenseID == "" || created.LicenseCode == "" || created.Revision != 0 {
		t.Fatalf("new code is not unactivated: %+v", created)
	}

	status, body = a.call(t, "GET", "/api/v1/admin/offline-licenses?deliveryState=pending&termState=unactivated", nil)
	pending := decodeLocal[struct {
		Items []store.OfflineLicenseView `json:"items"`
		Total int                        `json:"total"`
	}](t, body)
	if status != 200 || pending.Total != 1 || len(pending.Items) != 1 || pending.Items[0].TermState != "unactivated" || pending.Items[0].DeliveryState != "pending" {
		t.Fatalf("unactivated code was not pending delivery: %d %s", status, body)
	}
	status, body = a.call(t, "GET", "/api/v1/admin/overview", nil)
	before := decodeLocal[store.OfflineOverview](t, body)
	if status != 200 || before.Licenses != 1 || before.Valid != 0 || before.Expired != 0 || before.Expiring != 0 || before.PendingDelivery != 1 {
		t.Fatalf("unactivated perpetual code distorted overview: %d %+v %s", status, before, body)
	}

	const deliveryPath = "/api/v1/admin/offline-licenses/"
	deliveryPathForCode := deliveryPath + created.LicenseID + "/deliveries"
	delivery := map[string]string{"operationId": store.NewID(), "method": "wechat", "recipient": "合成授权码收件人", "deliveredAt": "2026-10-29T12:00:00.000Z", "notes": "授权码交付"}
	var deliveryID string
	replays := 0
	for range 2 {
		status, body = a.call(t, "POST", deliveryPathForCode, delivery)
		if status != 200 {
			t.Fatalf("record code delivery %d %s", status, body)
		}
		value := decodeLocal[struct {
			Delivery store.OfflineDelivery `json:"delivery"`
			Replayed bool                  `json:"replayed"`
		}](t, body)
		if value.Delivery.LicenseID != created.LicenseID || value.Delivery.IssuanceID != nil {
			t.Fatalf("code delivery must retain license identity without inventing an issuance: %+v", value.Delivery)
		}
		if deliveryID != "" && deliveryID != value.Delivery.ID {
			t.Fatal("replayed delivery created a duplicate row")
		}
		deliveryID = value.Delivery.ID
		if value.Replayed {
			replays++
		}
	}
	if replays != 1 {
		t.Fatalf("delivery operation replay count = %d", replays)
	}
	delivery["recipient"] = "不同收件人"
	if status, body = a.call(t, "POST", deliveryPathForCode, delivery); status != 409 {
		t.Fatalf("changed operation content was accepted: %d %s", status, body)
	}

	status, body = a.call(t, "GET", deliveryPathForCode, nil)
	rows := decodeLocal[struct {
		Items []store.OfflineDelivery `json:"items"`
	}](t, body)
	if status != 200 || len(rows.Items) != 1 || rows.Items[0].ID != deliveryID || rows.Items[0].IssuanceID != nil {
		t.Fatalf("license delivery history %d %s", status, body)
	}
	status, body = a.call(t, "GET", "/api/v1/admin/offline-licenses?deliveryState=recorded&termState=unactivated", nil)
	recorded := decodeLocal[struct {
		Items []store.OfflineLicenseView `json:"items"`
		Total int                        `json:"total"`
	}](t, body)
	if status != 200 || recorded.Total != 1 || len(recorded.Items) != 1 || recorded.Items[0].DeliveryState != "recorded" || recorded.Items[0].DeliveryMethod != "wechat" || recorded.Items[0].DeliveryRecipient != "合成授权码收件人" {
		t.Fatalf("delivery filter/summary did not reflect code delivery: %d %s", status, body)
	}
	status, body = a.call(t, "GET", "/api/v1/admin/offline-licenses/"+created.LicenseID, nil)
	detail := decodeLocal[struct {
		Deliveries []store.OfflineDelivery `json:"deliveries"`
	}](t, body)
	if status != 200 || len(detail.Deliveries) != 1 || detail.Deliveries[0].ID != deliveryID {
		t.Fatalf("license detail lost delivery history: %d %s", status, body)
	}
	status, body = a.call(t, "GET", "/api/v1/admin/overview", nil)
	after := decodeLocal[store.OfflineOverview](t, body)
	if status != 200 || after.PendingDelivery != 0 || after.Valid != 0 || after.Expired != 0 || after.Expiring != 0 {
		t.Fatalf("delivered unactivated code distorted overview: %d %+v %s", status, after, body)
	}
	var deliveryRows, auditRows int
	if err := a.store.DB.NewRaw("SELECT count(*) FROM license_deliveries WHERE license_id=?", created.LicenseID).Scan(context.Background(), &deliveryRows); err != nil || deliveryRows != 1 {
		t.Fatalf("delivery transaction left %d rows: %v", deliveryRows, err)
	}
	if err := a.store.DB.NewRaw("SELECT count(*) FROM audit_logs WHERE entity='license-delivery' AND entity_id=?", deliveryID).Scan(context.Background(), &auditRows); err != nil || auditRows != 1 {
		t.Fatalf("delivery audit rows = %d: %v", auditRows, err)
	}
}
