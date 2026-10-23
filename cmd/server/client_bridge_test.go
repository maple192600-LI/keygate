package main

// Integration-side bridge for the real Electron consumer. Never compiled into the server.
import (
	"encoding/json"
	"github.com/tabloy/keygate/internal/offline"
	"github.com/tabloy/keygate/internal/service"
	"github.com/tabloy/keygate/internal/store"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLocalOfflineElectronBridge(t *testing.T) {
	root := os.Getenv("SNAIL_ELECTRON_BRIDGE_ROOT")
	if root == "" {
		t.Skip("explicit task-owned Electron evidence directory required")
	}
	if !filepath.IsAbs(root) {
		t.Fatal("absolute isolated directory required")
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		t.Fatal("unsafe evidence directory")
	}
	a := localApp(t)
	ownerPassword := "Synthetic-bridge-owner-password"
	for _, step := range []struct {
		path   string
		body   any
		status int
	}{
		{"/api/v1/local/setup", map[string]string{"username": "bridge_owner", "password": ownerPassword, "siteName": "合成闭环厂商", "contact": "仅用于隔离验收"}, 201},
		{"/api/v1/auth/password/login", map[string]string{"username": "bridge_owner", "password": ownerPassword}, 200},
	} {
		if status, body := a.call(t, "POST", step.path, step.body); status != step.status {
			t.Fatalf("bridge setup %d %s", status, body)
		}
	}
	status, body := a.call(t, "POST", "/api/v1/admin/offline-key/create", map[string]string{"ownerPassword": ownerPassword, "passphrase": "Synthetic-bridge-key-password"})
	if status != 201 {
		t.Fatalf("identity %d %s", status, body)
	}
	identity := decodeLocal[offline.PublicIdentity](t, body)
	policy := map[string]any{"formatVersion": 1, "productId": offline.ProductID, "purpose": "commercial", "authorizationMode": "required", "licenseKey": map[string]string{"keyId": identity.KeyID, "publicKeyPem": identity.PublicKeyPEM}}
	bytes, _ := json.Marshal(policy)
	if err = os.WriteFile(filepath.Join(root, "public-policy.json"), bytes, 0600); err != nil {
		t.Fatal(err)
	}
	var request []byte
	deadline := time.Now().Add(100 * time.Second)
	for time.Now().Before(deadline) {
		if _, cancelErr := os.Stat(filepath.Join(root, "cancel")); cancelErr == nil {
			t.Fatal("Electron bridge canceled; cleaning isolated database")
		}
		request, err = os.ReadFile(filepath.Join(root, "client.snailreq"))
		if err == nil {
			break
		}
		if !os.IsNotExist(err) {
			t.Fatal(err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err != nil {
		t.Fatal("real client did not export its request")
	}
	if _, _, err = offline.ParseRequest(request, offline.ProductID); err != nil {
		t.Fatal(err)
	}
	status, body = a.call(t, "POST", "/api/v1/admin/commercial-customers", map[string]any{"displayName": "真实客户端闭环合成客户", "customerType": "company", "contactMethod": "合成联系电话", "email": nil})
	if status != 201 {
		t.Fatalf("customer %d %s", status, body)
	}
	customer := decodeLocal[struct {
		ID string `json:"id"`
	}](t, body)
	status, body = a.call(t, "POST", "/api/v1/admin/offline-requests", map[string]string{"requestFile": string(request)})
	if status != 200 {
		t.Fatalf("request %d %s", status, body)
	}
	imported := decodeLocal[struct {
		Request struct {
			ID string `json:"requestId"`
		} `json:"request"`
	}](t, body)
	intent := service.OfflineIntent{Action: "initial", CustomerID: customer.ID, RequestID: imported.Request.ID, Term: offline.Term{Kind: "days", Amount: 7}}
	status, body = a.call(t, "POST", "/api/v1/admin/offline-licenses/preview", intent)
	if status != 200 {
		t.Fatalf("preview %d %s", status, body)
	}
	preview := decodeLocal[service.OfflinePreview](t, body)
	command := service.OfflineIssueCommand{OfflineIntent: intent, OperationID: store.NewID(), PreviewBase: preview.PreviewBase}
	status, body = a.call(t, "POST", "/api/v1/admin/offline-licenses", command)
	if status != 200 {
		t.Fatalf("issue %d %s", status, body)
	}
	result := decodeLocal[service.OfflineIssueResult](t, body)
	status, body = a.call(t, "GET", "/api/v1/admin/license-issuances/"+result.IssuanceID+"/file", nil)
	if status != 200 {
		t.Fatal("original unavailable")
	}
	if err = os.WriteFile(filepath.Join(root, "client.snaillic"), body, 0600); err != nil {
		t.Fatal(err)
	}
	bytes, _ = json.Marshal(result)
	if err = os.WriteFile(filepath.Join(root, "issuance-result.json"), bytes, 0600); err != nil {
		t.Fatal(err)
	}
	// Move actual management data to a separately initialized empty instance.
	// The existing Electron policy remains untouched and trusts the same key.
	status, bundle := a.call(t, "POST", "/api/v1/admin/business-bundle/export", nil)
	if status != 200 {
		t.Fatalf("export %d %s", status, bundle)
	}
	status, encryptedIdentity := a.call(t, "GET", "/api/v1/admin/offline-key/encrypted", nil)
	if status != 200 {
		t.Fatal("identity export failed")
	}
	b := localApp(t)
	const targetPassword = "Synthetic-handoff-bridge-owner-password"
	for _, step := range []struct {
		path   string
		body   any
		status int
	}{
		{"/api/v1/local/setup", map[string]string{"username": "handoff_bridge_owner", "password": targetPassword, "siteName": "合成移交目标", "contact": "隔离验收"}, 201},
		{"/api/v1/auth/password/login", map[string]string{"username": "handoff_bridge_owner", "password": targetPassword}, 200},
	} {
		if status, body = b.call(t, "POST", step.path, step.body); status != step.status {
			t.Fatalf("target setup %d %s", status, body)
		}
	}
	status, body = b.upload(t, "/api/v1/admin/business-bundle/preview", bundle, "")
	if status != 200 {
		t.Fatalf("target preview %d %s", status, body)
	}
	handoff := decodeLocal[struct {
		ID      string              `json:"previewId"`
		Summary store.BundleSummary `json:"summary"`
	}](t, body)
	if handoff.Summary.Customers != 1 || handoff.Summary.Licenses != 1 || handoff.Summary.Originals != 1 || handoff.Summary.KeyID != identity.KeyID {
		t.Fatal("handoff summary mismatch")
	}
	status, body = b.call(t, "POST", "/api/v1/admin/business-bundle/import", map[string]string{"previewId": handoff.ID, "ownerPassword": targetPassword})
	if status != 200 {
		t.Fatalf("target import %d %s", status, body)
	}
	status, transferredOriginal := b.call(t, "GET", "/api/v1/admin/license-issuances/"+result.IssuanceID+"/file", nil)
	original, err := os.ReadFile(filepath.Join(root, "client.snaillic"))
	if err != nil || status != 200 || string(transferredOriginal) != string(original) {
		t.Fatal("target changed original bytes", err)
	}
	status, body = b.call(t, "POST", "/api/v1/admin/offline-key/import/preview", map[string]string{"identityFile": string(encryptedIdentity), "passphrase": "Synthetic-bridge-key-password"})
	if status != 200 {
		t.Fatalf("target key preview %d %s", status, body)
	}
	keyPreview := decodeLocal[struct {
		ID string `json:"previewId"`
	}](t, body)
	status, body = b.call(t, "POST", "/api/v1/admin/offline-key/import", map[string]string{"previewId": keyPreview.ID, "ownerPassword": targetPassword})
	if status != 200 {
		t.Fatalf("target key import %d %s", status, body)
	}
	status, body = b.call(t, "POST", "/api/v1/admin/offline-key/unlock", map[string]string{"passphrase": "Synthetic-bridge-key-password"})
	if status != 200 {
		t.Fatalf("target unlock %d %s", status, body)
	}
	renewIntent := service.OfflineIntent{Action: "renew", LicenseID: result.LicenseID, ExpectedRevision: result.Revision, Term: offline.Term{Kind: "days", Amount: 7}}
	status, body = b.call(t, "POST", "/api/v1/admin/offline-licenses/preview", renewIntent)
	if status != 200 {
		t.Fatalf("target renewal preview %d %s", status, body)
	}
	renewPreview := decodeLocal[service.OfflinePreview](t, body)
	status, body = b.call(t, "POST", "/api/v1/admin/offline-licenses/"+result.LicenseID+"/renew", service.OfflineIssueCommand{OfflineIntent: renewIntent, OperationID: store.NewID(), PreviewBase: renewPreview.PreviewBase})
	if status != 200 {
		t.Fatalf("target renewal %d %s", status, body)
	}
	renewed := decodeLocal[service.OfflineIssueResult](t, body)
	if renewed.LicenseID != result.LicenseID || renewed.Revision != 2 || renewed.KeyID != result.KeyID {
		t.Fatal("handoff renewal contract changed")
	}
	oldEnd, _ := time.Parse(time.RFC3339Nano, *result.ExpiresAt)
	newEnd, _ := time.Parse(time.RFC3339Nano, *renewed.ExpiresAt)
	if newEnd.Sub(oldEnd) != 7*24*time.Hour {
		t.Fatal("handoff lost original remaining term")
	}
	status, body = b.call(t, "GET", "/api/v1/admin/license-issuances/"+renewed.IssuanceID+"/file", nil)
	if status != 200 {
		t.Fatal("target renewed original unavailable")
	}
	if err = os.WriteFile(filepath.Join(root, "handoff-renewal.snaillic"), body, 0600); err != nil {
		t.Fatal(err)
	}
	bytes, _ = json.Marshal(renewed)
	if err = os.WriteFile(filepath.Join(root, "handoff-result.json"), bytes, 0600); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("SNAIL_ELECTRON_BRIDGE_RECOVERY") == "1" {
		deadline := time.Now().Add(60 * time.Second)
		var repair []byte
		for time.Now().Before(deadline) {
			repair, err = os.ReadFile(filepath.Join(root, "client-repair.snailreq"))
			if err == nil {
				break
			}
			if _, e := os.Stat(filepath.Join(root, "cancel")); e == nil {
				t.Fatal("repair bridge canceled")
			}
			time.Sleep(50 * time.Millisecond)
		}
		if err != nil {
			t.Fatal("client repair request not exported")
		}
		status, body = b.call(t, "POST", "/api/v1/admin/offline-key/recovery", map[string]string{"requestFile": string(repair)})
		if status != 200 {
			t.Fatalf("repair signing %d %s", status, body)
		}
		if err = os.WriteFile(filepath.Join(root, "client-repair.snailrepair"), body, 0600); err != nil {
			t.Fatal(err)
		}
	}
}
