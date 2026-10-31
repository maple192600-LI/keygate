package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/tabloy/keygate/internal/config"
	"github.com/tabloy/keygate/internal/offline"
	"github.com/tabloy/keygate/internal/service"
	"github.com/tabloy/keygate/internal/store"
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
	targetStore := localTestStore(t)
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
	status, body = a.call(t, "GET", "/api/v1/admin/offline-key/public", nil)
	if status != 200 {
		t.Fatalf("fresh signing identity status %d %s", status, body)
	}
	freshIdentity := decodeLocal[struct {
		Identity struct {
			KeyID        string `json:"keyId"`
			PublicKeyPEM string `json:"publicKeyPem"`
		} `json:"identity"`
		AutomaticSigning bool `json:"automaticSigning"`
	}](t, body)
	if !freshIdentity.AutomaticSigning || freshIdentity.Identity.KeyID != "" || freshIdentity.Identity.PublicKeyPEM != "" {
		t.Fatalf("a fresh target registered an unused signing identity before issuance: %+v", freshIdentity)
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
	onlineIntent := service.OfflineIntent{Action: "initial", CustomerID: customer.ID, DeviceLimit: 1, Term: offline.Term{Kind: "days", Amount: 7}}
	status, body = a.call(t, "POST", "/api/v1/admin/offline-licenses/preview", onlineIntent)
	if status != 200 {
		t.Fatalf("online code preview %d %s", status, body)
	}
	onlinePreview := decodeLocal[service.OfflinePreview](t, body)
	onlineCommand := service.OfflineIssueCommand{OfflineIntent: onlineIntent, OperationID: "761d5883-cec0-4e35-9aa6-4d0717b3adbc", PreviewBase: onlinePreview.PreviewBase}
	status, body = a.call(t, "POST", "/api/v1/admin/offline-licenses", onlineCommand)
	if status != 200 {
		t.Fatalf("online code issue %d %s", status, body)
	}
	issued := decodeLocal[service.OfflineIssueResult](t, body)
	if issued.LicenseID == "" || issued.LicenseCode == "" || issued.Revision != 0 {
		t.Fatalf("online code was not preserved unactivated: %+v", issued)
	}
	status, body = a.call(t, "GET", "/api/v1/admin/issuance-operations/"+onlineCommand.OperationID, nil)
	if status != 200 {
		t.Fatalf("recover lost create response from operation history %d %s", status, body)
	}
	onlineLicense := decodeLocal[service.OfflineIssueResult](t, body)
	var operationEnvelope struct {
		Data map[string]json.RawMessage `json:"data"`
	}
	if err = json.Unmarshal(body, &operationEnvelope); err != nil {
		t.Fatal("decode create operation response", err)
	}
	if onlineLicense.LicenseID != issued.LicenseID || onlineLicense.CustomerNumber != issued.CustomerNumber || onlineLicense.LicenseNumber != issued.LicenseNumber || onlineLicense.DisplayNumber != issued.DisplayNumber || onlineLicense.DeviceLimit != issued.DeviceLimit || onlineLicense.Revision != 0 || !onlineLicense.Replayed {
		t.Fatalf("operation query did not return the original create result: issued=%+v recovered=%+v", issued, onlineLicense)
	}
	if _, leaked := operationEnvelope.Data["licenseCode"]; leaked || onlineLicense.LicenseCode != "" {
		t.Fatal("operation history exposed a plaintext activation code")
	}
	status, body, headers := a.callHeaders(t, "GET", "/api/v1/admin/offline-licenses/"+onlineLicense.LicenseID+"/code", nil)
	if status != 200 || !strings.Contains(strings.ToLower(headers.Get("Cache-Control")), "no-store") {
		t.Fatalf("owner activation code retrieval status/cache policy %d %v %s", status, headers, body)
	}
	retrievedCode := decodeLocal[struct {
		LicenseCode string `json:"licenseCode"`
	}](t, body)
	if retrievedCode.LicenseCode != issued.LicenseCode {
		t.Fatal("owner code endpoint did not recover the original created code")
	}
	onlineLicense.LicenseCode = retrievedCode.LicenseCode
	const businessPassword = "Synthetic-business-package-password"
	status, businessArchive, businessHeaders := a.callHeaders(t, "POST", "/api/v1/admin/business-bundle/export", map[string]string{"password": businessPassword})
	if status != 200 || !strings.HasPrefix(businessHeaders.Get("Content-Type"), "application/octet-stream") || !strings.Contains(strings.ToLower(businessHeaders.Get("Cache-Control")), "no-store") {
		t.Fatalf("owner business export response status/content-cache policy %d %v", status, businessHeaders)
	}
	if bytes.Contains(businessArchive, []byte(onlineLicense.LicenseCode)) {
		t.Fatal("business export response exposed the activation code without decryption")
	}
	businessPlain, err := offline.OpenBackup(businessArchive, businessPassword)
	if err != nil {
		t.Fatal("open sealed business export", err)
	}
	var businessPackage struct {
		FormatVersion int               `json:"formatVersion"`
		Kind          string            `json:"kind"`
		LicenseCodes  map[string]string `json:"licenseCodes"`
	}
	if err = json.Unmarshal(businessPlain, &businessPackage); err != nil {
		clear(businessPlain)
		t.Fatal("decode sealed business payload", err)
	}
	clear(businessPlain)
	if businessPackage.FormatVersion != 1 || businessPackage.Kind != "snail-commercial-transfer" || businessPackage.LicenseCodes[onlineLicense.LicenseID] != onlineLicense.LicenseCode {
		t.Fatal("sealed business export omitted the original online activation code")
	}
	status, body = a.call(t, "GET", "/api/v1/admin/offline-licenses/"+onlineLicense.LicenseID, nil)
	if status != 200 {
		t.Fatalf("unactivated online license detail %d %s", status, body)
	}
	unactivated := decodeLocal[struct {
		License struct {
			Revision  int64   `json:"revision"`
			TermState string  `json:"termState"`
			NotBefore *string `json:"notBefore"`
			ExpiresAt *string `json:"expiresAt"`
		} `json:"license"`
	}](t, body).License
	if unactivated.Revision != 0 || unactivated.TermState != "unactivated" || unactivated.NotBefore != nil || unactivated.ExpiresAt != nil {
		t.Fatalf("unactivated code showed a fabricated start or deadline: %+v", unactivated)
	}
	status, body = a.call(t, "GET", "/api/v1/admin/offline-key/public", nil)
	if status != 200 {
		t.Fatalf("automatic signing identity after first issuance %d %s", status, body)
	}
	identityBeforeRestore := decodeLocal[struct {
		Identity struct {
			KeyID        string `json:"keyId"`
			PublicKeyPEM string `json:"publicKeyPem"`
		} `json:"identity"`
		AutomaticSigning bool `json:"automaticSigning"`
	}](t, body)
	if !identityBeforeRestore.AutomaticSigning || identityBeforeRestore.Identity.KeyID == "" || identityBeforeRestore.Identity.PublicKeyPEM == "" {
		t.Fatalf("automatic signing identity was not established at first issuance: %+v", identityBeforeRestore)
	}
	businessRaw, err := s.ExportBusinessBundle(context.Background(), offline.PublicIdentity{KeyID: identityBeforeRestore.Identity.KeyID, PublicKeyPEM: identityBeforeRestore.Identity.PublicKeyPEM, State: "public"})
	if err != nil {
		t.Fatalf("export with unactivated online code %v", err)
	}
	for _, malformed := range []string{"current-pointer", "deadline", "binding"} {
		bundle, parseErr := store.ParseBusinessBundle(businessRaw)
		if parseErr != nil {
			t.Fatalf("parse exported unactivated bundle: %v", parseErr)
		}
		var licenseRow store.BundleRow
		for _, row := range bundle.Tables["licenses"] {
			var id string
			_ = json.Unmarshal(row["id"], &id)
			if id == onlineLicense.LicenseID {
				licenseRow = row
				break
			}
		}
		if licenseRow == nil {
			t.Fatal("unactivated license missing from business bundle")
		}
		switch malformed {
		case "current-pointer":
			licenseRow["current_issuance_id"], _ = json.Marshal(store.NewID())
		case "deadline":
			licenseRow["valid_until"], _ = json.Marshal("2026-11-05T00:00:00Z")
		case "binding":
			binding := store.BundleRow{}
			for key, value := range bundle.Tables["offline_device_bindings"][0] {
				binding[key] = append(json.RawMessage(nil), value...)
			}
			binding["id"], _ = json.Marshal(store.NewID())
			binding["license_id"], _ = json.Marshal(onlineLicense.LicenseID)
			bundle.Tables["offline_device_bindings"] = append(bundle.Tables["offline_device_bindings"], binding)
		}
		if err = store.ValidateBusinessBundle(bundle); err == nil {
			t.Fatalf("unactivated license accepted malformed %s state", malformed)
		}
	}
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
	if status, body = a.call(t, "POST", "/api/v1/admin/data-previews/cancel", map[string]string{"previewId": restore.ID}); status != 200 {
		t.Fatalf("cancel source-side restore candidate %d %s", status, body)
	}
	// Stop the source listener before the restored environment accepts writes.
	server.Close()

	targetServer := httptest.NewUnstartedServer(nil)
	targetAddress := targetServer.Listener.Addr().String()
	_, targetPort, _ := strings.Cut(targetAddress, ":")
	targetDataRoot := t.TempDir()
	targetDSN, err := url.Parse(os.Getenv("SNAIL_LOCAL_TEST_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	var targetDBName string
	if err = targetStore.DB.NewRaw("SELECT current_database()").Scan(context.Background(), &targetDBName); err != nil {
		t.Fatal(err)
	}
	targetDSN.Path = "/" + targetDBName
	targetCfg := &config.Config{Port: targetPort, BaseURL: "http://" + targetAddress, Environment: "production", SnailDeployment: "local_offline", SnailDataDir: targetDataRoot, SnailRuntimeToken: strings.Repeat("8", 64), JWTSecret: strings.Repeat("7", 64), ReleaseKeyEncryptionKey: strings.Repeat("6", 64), DatabaseURL: targetDSN.String()}
	targetRuntime := map[string]any{"formatVersion": 1, "databasePassword": strings.Repeat("5", 64), "databasePort": 55440, "appPort": 9001, "jwtSecret": targetCfg.JWTSecret, "encryptionSecret": targetCfg.ReleaseKeyEncryptionKey}
	targetRuntimeRaw, _ := json.Marshal(targetRuntime)
	targetRuntimeDigest := sha256.Sum256(targetRuntimeRaw)
	targetCfg.SnailRuntimeConfigSHA256 = hex.EncodeToString(targetRuntimeDigest[:])
	targetRuntimeFile := filepath.Join(targetDataRoot, "runtime.json")
	if err = os.WriteFile(targetRuntimeFile, targetRuntimeRaw, 0600); err != nil {
		t.Fatal(err)
	}
	targetManaged := newLocalRouterSwitch(targetCfg, targetStore, func() {})
	targetServer.Config.Handler = targetManaged
	targetServer.Start()
	t.Cleanup(func() {
		targetServer.Close()
		if err := targetManaged.Close(); err != nil {
			t.Error(err)
		}
	})
	targetJar, _ := cookiejar.New(nil)
	targetApp := &localTestApp{server: targetServer, client: &http.Client{Jar: targetJar}, store: targetStore, runtimeToken: targetCfg.SnailRuntimeToken}
	targetPassword := "Synthetic-target-owner-password"
	if status, body = targetApp.call(t, "POST", "/api/v1/local/setup", map[string]string{"username": "target_owner", "password": targetPassword, "siteName": "合成恢复目标", "contact": "合成目标联系号"}); status != 201 {
		t.Fatalf("target setup %d %s", status, body)
	}
	if status, body = targetApp.call(t, "POST", "/api/v1/auth/password/login", map[string]string{"username": "target_owner", "password": targetPassword}); status != 200 {
		t.Fatalf("target login %d %s", status, body)
	}
	status, body = targetApp.call(t, "GET", "/api/v1/admin/offline-key/public", nil)
	if status != 200 {
		t.Fatalf("fresh target identity %d %s", status, body)
	}
	freshTargetIdentity := decodeLocal[struct {
		Identity struct {
			KeyID string `json:"keyId"`
		} `json:"identity"`
		AutomaticSigning bool `json:"automaticSigning"`
	}](t, body)
	if !freshTargetIdentity.AutomaticSigning || freshTargetIdentity.Identity.KeyID != "" {
		t.Fatalf("fresh destination pre-registered a conflicting identity: %+v", freshTargetIdentity)
	}
	targetParsedURL, _ := url.Parse(targetServer.URL)
	targetOldCookies := targetApp.client.Jar.Cookies(targetParsedURL)
	status, body = targetApp.upload(t, "/api/v1/admin/backups/preview", archive, backupPassword)
	if status != 200 {
		t.Fatalf("target restore preview %d %s", status, body)
	}
	targetRestore := decodeLocal[struct {
		ID      string `json:"previewId"`
		Summary struct {
			Customers int `json:"customers"`
			Licenses  int `json:"licenses"`
			Originals int `json:"originals"`
		} `json:"summary"`
	}](t, body)
	if targetRestore.Summary.Customers != 1 || targetRestore.Summary.Licenses != 2 || targetRestore.Summary.Originals != 1 {
		t.Fatalf("target preview lost unactivated online code records: %+v", targetRestore.Summary)
	}
	if status, body = targetApp.call(t, "POST", "/api/v1/admin/backups/restore", map[string]string{"previewId": targetRestore.ID, "ownerPassword": targetPassword}); status != 200 {
		t.Fatalf("restore into independent target %d %s", status, body)
	}
	if status, _ = targetApp.call(t, "GET", "/api/v1/auth/me", nil); status != 401 {
		t.Fatal("target session survived replacement by the restored owner account")
	}
	targetOldJar, _ := cookiejar.New(nil)
	targetOldJar.SetCookies(targetParsedURL, targetOldCookies)
	targetOldClient := &localTestApp{server: targetServer, client: &http.Client{Jar: targetOldJar}}
	if status, _ = targetOldClient.call(t, "POST", "/api/v1/auth/refresh", nil); status != 401 {
		t.Fatal("target refresh session survived restore")
	}
	if status, _ = targetApp.call(t, "POST", "/api/v1/auth/password/login", map[string]string{"username": "restore_owner", "password": targetPassword}); status != 401 {
		t.Fatal("target password survived restore")
	}
	if status, body = targetApp.call(t, "POST", "/api/v1/auth/password/login", map[string]string{"username": "restore_owner", "password": ownerPassword}); status != 200 {
		t.Fatalf("restored owner login on target %d %s", status, body)
	}
	status, body = targetApp.call(t, "GET", "/api/v1/admin/offline-key/public", nil)
	if status != 200 {
		t.Fatalf("restored target signing identity %d %s", status, body)
	}
	identityAfterRestore := decodeLocal[struct {
		Identity struct {
			KeyID        string `json:"keyId"`
			PublicKeyPEM string `json:"publicKeyPem"`
		} `json:"identity"`
		AutomaticSigning bool `json:"automaticSigning"`
	}](t, body)
	if !identityAfterRestore.AutomaticSigning || identityAfterRestore.Identity.KeyID != identityBeforeRestore.Identity.KeyID || identityAfterRestore.Identity.PublicKeyPEM != identityBeforeRestore.Identity.PublicKeyPEM {
		t.Fatalf("restored destination lost the source signing identity: before=%+v after=%+v", identityBeforeRestore, identityAfterRestore)
	}
	status, body = targetApp.call(t, "GET", "/api/v1/admin/offline-licenses/"+onlineLicense.LicenseID+"/code", nil)
	if status != 200 {
		t.Fatalf("restored online code retrieval %d %s", status, body)
	}
	restoredCode := decodeLocal[struct {
		LicenseCode string `json:"licenseCode"`
	}](t, body)
	if restoredCode.LicenseCode != onlineLicense.LicenseCode {
		t.Fatal("restored environment changed the original activation code")
	}
	clientServer := httptest.NewServer(targetManaged.ClientHandler())
	defer clientServer.Close()
	clientApp := &localTestApp{clientServer: clientServer, publicClient: &http.Client{}}
	device := offline.Device{Type: offline.MachineType, Digest: strings.Repeat("a", 64)}
	activation := map[string]any{
		"operationId": "1ecac4a4-6117-427b-922c-799ef8ecb911",
		"licenseCode": onlineLicense.LicenseCode,
		"productId":   offline.ProductID,
		"device":      device,
		"deviceLabel": "合成恢复后的设备",
		"appVersion":  "0.1.0-test",
		"buildId":     "restore-t16",
	}
	status, body = clientApp.callClient(t, "POST", "/api/v1/client/activate", activation)
	if status != 200 {
		t.Fatalf("restored online activation %d %s", status, body)
	}
	activated := decodeLocal[service.ClientLicenseResult](t, body)
	if activated.RawLicense == "" || activated.LicenseID != onlineLicense.LicenseID || activated.ServerTime == "" {
		t.Fatalf("restored activation result identity: %+v", activated)
	}
	status, body = clientApp.callClient(t, "POST", "/api/v1/client/activate", activation)
	if status != 200 {
		t.Fatalf("restored activation replay %d %s", status, body)
	}
	activationReplay := decodeLocal[service.ClientLicenseResult](t, body)
	if activationReplay.RawLicense != activated.RawLicense || activationReplay.BindingID != activated.BindingID {
		t.Fatal("restored activation replay changed the signed grant or seat")
	}
	activation["operationId"] = "c511fbd9-958a-4261-8d3d-3b727176a03d"
	status, body = clientApp.callClient(t, "POST", "/api/v1/client/activate", activation)
	if status != 200 {
		t.Fatalf("restored same-device activation %d %s", status, body)
	}
	sameDevice := decodeLocal[service.ClientLicenseResult](t, body)
	if sameDevice.RawLicense != activated.RawLicense || sameDevice.BindingID != activated.BindingID {
		t.Fatal("restored same-device activation changed its signed grant or seat")
	}
	status, body = clientApp.callClient(t, "POST", "/api/v1/client/sync", map[string]any{
		"rawLicense":  activated.RawLicense,
		"device":      device,
		"deviceLabel": "合成恢复后的设备",
		"appVersion":  "0.1.0-test",
		"buildId":     "restore-t16",
	})
	if status != 200 {
		t.Fatalf("restored client sync %d %s", status, body)
	}
	synced := decodeLocal[service.ClientLicenseResult](t, body)
	public, err := offline.ParsePublic([]byte(identityAfterRestore.Identity.PublicKeyPEM))
	if err != nil {
		t.Fatal("restored public identity", err)
	}
	payload, err := offline.Verify([]byte(synced.RawLicense), map[string]ed25519.PublicKey{identityAfterRestore.Identity.KeyID: public}, offline.ProductID)
	if err != nil || payload.BindingState != "active" || payload.Revision != 1 || payload.LicenseID != onlineLicense.LicenseID || synced.RawLicense != activated.RawLicense {
		t.Fatalf("restored sync did not retain the signed identity and activation: payload=%+v error=%v", payload, err)
	}
	var sourceRevision, sourceBindings, targetRevision, targetBindings int64
	if err = s.DB.NewRaw("SELECT current_revision FROM licenses WHERE id=?", onlineLicense.LicenseID).Scan(context.Background(), &sourceRevision); err != nil {
		t.Fatal("source online ledger", err)
	}
	if err = s.DB.NewRaw("SELECT count(*) FROM offline_device_bindings WHERE license_id=?", onlineLicense.LicenseID).Scan(context.Background(), &sourceBindings); err != nil {
		t.Fatal("source online bindings", err)
	}
	targetDB := targetManaged.owned[len(targetManaged.owned)-1].DB
	if err = targetDB.NewRaw("SELECT current_revision FROM licenses WHERE id=?", onlineLicense.LicenseID).Scan(context.Background(), &targetRevision); err != nil {
		t.Fatal("restored online ledger", err)
	}
	if err = targetDB.NewRaw("SELECT count(*) FROM offline_device_bindings WHERE license_id=? AND superseded_at IS NULL", onlineLicense.LicenseID).Scan(context.Background(), &targetBindings); err != nil {
		t.Fatal("restored online bindings", err)
	}
	if sourceRevision != 0 || sourceBindings != 0 || targetRevision != 1 || targetBindings != 1 {
		t.Fatalf("online activation did not write only to the restored environment: source=(revision %d, bindings %d), target=(revision %d, bindings %d)", sourceRevision, sourceBindings, targetRevision, targetBindings)
	}
	status, body = targetApp.call(t, "GET", "/api/v1/admin/commercial-customers", nil)
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
	raw, err := os.ReadFile(targetRuntimeFile)
	if err != nil {
		t.Fatal(err)
	}
	var saved map[string]any
	_ = json.Unmarshal(raw, &saved)
	if saved["databaseName"] == nil || saved["identityDirectory"] == nil || saved["jwtSecret"] == targetCfg.JWTSecret {
		t.Fatal("target restore not persisted for native next launch")
	}
	savedDigest := sha256.Sum256(raw)
	if targetManaged.cfg.SnailRuntimeConfigSHA256 != hex.EncodeToString(savedDigest[:]) {
		t.Fatal("target next launch does not use newly published runtime digest")
	}
	if err = targetManaged.owned[len(targetManaged.owned)-1].DB.NewRaw("SELECT count(*) FROM audit_logs WHERE entity='backup' AND entity_id=? AND action='restore'", targetRestore.ID).Scan(context.Background(), &count); err != nil || count != 1 {
		t.Fatal("target restore retry duplicated audit", count, err)
	}
	if err = s.DB.NewRaw("SELECT count(*) FROM commercial_customers").Scan(context.Background(), &count); err != nil || count != 2 {
		t.Fatal("old source database not preserved", count, err)
	}
}
