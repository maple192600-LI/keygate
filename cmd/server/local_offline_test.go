package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/tabloy/keygate/internal/config"
	"github.com/tabloy/keygate/internal/store"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func localTestStore(t *testing.T) *store.Store {
	t.Helper()
	dsn := os.Getenv("SNAIL_LOCAL_TEST_DSN")
	if dsn == "" {
		t.Skip("SNAIL_LOCAL_TEST_DSN must name the task-owned PostgreSQL test instance")
	}
	parent, err := store.New(dsn)
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	name := "snail_t03_" + strings.ReplaceAll(store.NewID(), "-", "")
	if _, err = parent.DB.ExecContext(context.Background(), "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	child, err := store.New(u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := child.Close(); err != nil {
			t.Error(err)
		}
		if _, err := parent.DB.ExecContext(context.Background(), "DROP DATABASE "+name); err != nil {
			t.Error(err)
		}
		if err := parent.Close(); err != nil {
			t.Error(err)
		}
	})
	if err = child.RunMigrations("../../db/migrations"); err != nil {
		t.Fatal(err)
	}
	return child
}

type localTestApp struct {
	server       *httptest.Server
	client       *http.Client
	store        *store.Store
	runtimeToken string
}

func localApp(t *testing.T) *localTestApp {
	t.Helper()
	s := localTestStore(t)
	server := httptest.NewUnstartedServer(nil)
	address := server.Listener.Addr().String()
	_, port, _ := strings.Cut(address, ":")
	cfg := &config.Config{Port: port, BaseURL: "http://" + address, Environment: "production", SnailDeployment: "local_offline", SnailDataDir: t.TempDir(), SnailRuntimeToken: strings.Repeat("c", 64), JWTSecret: strings.Repeat("s", 64), ReleaseKeyEncryptionKey: strings.Repeat("a", 64)}
	server.Config.Handler = newLocalOfflineRouter(cfg, s)
	server.Start()
	t.Cleanup(server.Close)
	jar, _ := cookiejar.New(nil)
	return &localTestApp{server, &http.Client{Jar: jar}, s, cfg.SnailRuntimeToken}
}
func (a *localTestApp) call(t *testing.T, method, path string, body any) (int, []byte) {
	t.Helper()
	raw, _ := json.Marshal(body)
	r, err := http.NewRequest(method, a.server.URL+path, bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", a.server.URL)
	if path == "/api/v1/local/setup" {
		r.Header.Set("X-Snail-Setup-Token", a.runtimeToken)
	}
	resp, err := a.client.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, data
}
func TestLocalOfflineSetupAuthenticationAndRevocation(t *testing.T) {
	a := localApp(t)
	setup := map[string]any{"username": "Synthetic_Owner", "password": "Synthetic-owner-password-2026", "siteName": "合成厂商", "contact": "合成联系号 010-00000000"}
	var wg sync.WaitGroup
	statuses := make(chan int, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			status, _ := a.call(t, "POST", "/api/v1/local/setup", setup)
			statuses <- status
		}()
	}
	wg.Wait()
	close(statuses)
	created, conflict := 0, 0
	for status := range statuses {
		if status == 201 {
			created++
		}
		if status == 409 {
			conflict++
		}
	}
	if created != 1 || conflict != 1 {
		t.Fatalf("setup race created=%d conflict=%d", created, conflict)
	}
	var owners, emptyEmail int
	if err := a.store.DB.NewRaw("SELECT count(*) FROM users WHERE role='owner'").Scan(context.Background(), &owners); err != nil {
		t.Fatal(err)
	}
	if err := a.store.DB.NewRaw("SELECT count(*) FROM users WHERE email IS NULL AND username='synthetic_owner'").Scan(context.Background(), &emptyEmail); err != nil {
		t.Fatal(err)
	}
	if owners != 1 || emptyEmail != 1 {
		t.Fatal("owner identity or email contract differs")
	}
	if status, body := a.call(t, "POST", "/api/v1/local/setup", setup); status != 409 || !bytes.Contains(body, []byte("SETUP_COMPLETE")) {
		t.Fatalf("repeated setup %d %s", status, body)
	}
	var denial []byte
	for _, username := range []string{"synthetic_owner", "unknown_owner"} {
		status, body := a.call(t, "POST", "/api/v1/auth/password/login", map[string]string{"username": username, "password": "wrong-long-password"})
		if status != 401 {
			t.Fatalf("bad login %d", status)
		}
		if denial != nil && !bytes.Equal(denial, body) {
			t.Fatal("login reveals which account exists")
		}
		denial = body
	}
	if status, body := a.call(t, "POST", "/api/v1/auth/password/login", map[string]string{"username": "SYNTHETIC_OWNER", "password": setup["password"].(string)}); status != 200 || bytes.Contains(body, []byte("password_hash")) || !bytes.Contains(body, []byte(`"email":null`)) {
		t.Fatalf("password login %d %s", status, body)
	}
	u, _ := url.Parse(a.server.URL)
	oldCookies := a.client.Jar.Cookies(u)
	if status, _ := a.call(t, "GET", "/api/v1/auth/me", nil); status != 200 {
		t.Fatal("owner session rejected")
	}
	if status, body := a.call(t, "POST", "/api/v1/auth/refresh", nil); status != 200 {
		t.Fatalf("refresh %d %s", status, body)
	}
	if status, body := a.call(t, "POST", "/api/v1/auth/password/change", map[string]string{"currentPassword": setup["password"].(string), "newPassword": "New-synthetic-password-2026"}); status != 200 {
		t.Fatalf("password change %d %s", status, body)
	}
	// Re-present the old session cookie explicitly, independently of browser deletion.
	req, _ := http.NewRequest("GET", a.server.URL+"/api/v1/auth/me", nil)
	for _, cookie := range oldCookies {
		if cookie.Name == "session" {
			req.AddCookie(cookie)
		}
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatal("old JWT survived password change")
	}
	if status, _ := a.call(t, "POST", "/api/v1/auth/password/login", map[string]string{"username": "synthetic_owner", "password": "New-synthetic-password-2026"}); status != 200 {
		t.Fatal("new password rejected")
	}
	if status, _ := a.call(t, "POST", "/api/v1/auth/logout", nil); status != 200 {
		t.Fatal("logout failed")
	}
	if status, _ := a.call(t, "GET", "/api/v1/auth/me", nil); status != 401 {
		t.Fatal("logout left session valid")
	}
}

func TestLocalOfflineWindowCapabilityAndReadiness(t *testing.T) {
	a := localApp(t)
	setup := map[string]string{"username": "window_owner", "password": "Synthetic-window-test-password", "siteName": "合成厂商", "contact": "合成联系号"}
	for _, origin := range []string{"", a.server.URL} {
		for _, token := range []string{"", strings.Repeat("0", 64)} {
			raw, _ := json.Marshal(setup)
			r, _ := http.NewRequest("POST", a.server.URL+"/api/v1/local/setup", bytes.NewReader(raw))
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Origin", origin)
			r.Header.Set("X-Snail-Setup-Token", token)
			response, err := a.client.Do(r)
			if err != nil {
				t.Fatal(err)
			}
			response.Body.Close()
			if response.StatusCode != 403 {
				t.Fatalf("setup accepted without window capability: %d", response.StatusCode)
			}
		}
	}
	var owners int
	if err := a.store.DB.NewRaw("SELECT count(*) FROM users WHERE role='owner'").Scan(context.Background(), &owners); err != nil || owners != 0 {
		t.Fatalf("denied setup changed owner: %d %v", owners, err)
	}
	if status, body := a.call(t, "POST", "/api/v1/local/setup", setup); status != 201 {
		t.Fatalf("window setup %d %s", status, body)
	}
	challenge := strings.Repeat("e", 64)
	status, body := a.call(t, "GET", "/health?challenge="+challenge, nil)
	var result struct {
		Data struct {
			Proof string `json:"proof"`
		} `json:"data"`
	}
	if status != 200 || json.Unmarshal(body, &result) != nil {
		t.Fatalf("readiness %d %s", status, body)
	}
	key, _ := hex.DecodeString(a.runtimeToken)
	nonce, _ := hex.DecodeString(challenge)
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte("snail-runtime-ready/v1\x00"))
	mac.Write(nonce)
	if result.Data.Proof != hex.EncodeToString(mac.Sum(nil)) {
		t.Fatal("readiness is not bound to this child capability")
	}
}
func TestLocalOfflineOnlyRoutesAndOrigin(t *testing.T) {
	a := localApp(t)
	for _, path := range []string{"/api/v1/license/activate", "/api/v1/auth/otp/send", "/api/v1/setup/initialize", "/api/v1/admin/run-expiry-checks", "/api/v1/admin/run-metered-sync"} {
		if status, body := a.call(t, "POST", path, nil); status != 404 {
			t.Fatalf("online route registered: %s %d %s", path, status, body)
		}
	}
	for _, test := range []struct{ origin, host string }{{"http://malicious.invalid", ""}, {"", "malicious.invalid:9000"}} {
		r, _ := http.NewRequest("GET", a.server.URL+"/health", nil)
		r.Header.Set("Origin", test.origin)
		if test.host != "" {
			r.Host = test.host
		}
		response, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if response.StatusCode != 403 {
			t.Fatal(fmt.Sprintf("host/origin accepted: %d %s", response.StatusCode, data))
		}
	}
	response, err := http.Get(a.server.URL + "/health")
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != 200 || response.Header.Get("X-Powered-By") != "Keygate" {
		t.Fatal("health or attribution differs")
	}
}

func TestLocalOfflineCustomersAndRequests(t *testing.T) {
	a := localApp(t)
	setup := map[string]string{"username": "customer_owner", "password": "Synthetic-customer-password-2026", "siteName": "合成厂商", "contact": "合成联系电话"}
	if status, body := a.call(t, "POST", "/api/v1/local/setup", setup); status != 201 {
		t.Fatalf("setup %d %s", status, body)
	}
	if status, body := a.call(t, "POST", "/api/v1/auth/password/login", map[string]string{"username": setup["username"], "password": setup["password"]}); status != 200 {
		t.Fatalf("login %d %s", status, body)
	}
	draft := map[string]any{"displayName": "无邮箱合成客户", "customerType": "company", "contactName": "合成联系人", "contactMethod": "合成联系电话", "email": nil, "sourceText": "客户介绍", "notes": "隔离业务样本"}
	customerIDs := []string{}
	for range 2 {
		status, body := a.call(t, "POST", "/api/v1/admin/commercial-customers", draft)
		if status != 201 {
			t.Fatalf("customer %d %s", status, body)
		}
		var envelope struct {
			Data struct {
				ID    string  `json:"id"`
				Email *string `json:"email"`
			} `json:"data"`
		}
		if err := json.Unmarshal(body, &envelope); err != nil {
			t.Fatal(err)
		}
		if envelope.Data.Email != nil || envelope.Data.ID == "" {
			t.Fatal("customer identity or nullable email differs")
		}
		customerIDs = append(customerIDs, envelope.Data.ID)
	}
	if customerIDs[0] == customerIDs[1] {
		t.Fatal("same names were merged")
	}
	draft["notes"] = "更新备注"
	if status, _ := a.call(t, "PATCH", "/api/v1/admin/commercial-customers/"+customerIDs[0], draft); status != 200 {
		t.Fatal("customer update failed")
	}
	status, body := a.call(t, "GET", "/api/v1/admin/commercial-customers?q="+url.QueryEscape("无邮箱合成客户"), nil)
	if status != 200 || !bytes.Contains(body, []byte(`"total":2`)) {
		t.Fatalf("customer query %d %s", status, body)
	}
	requestRaw, err := os.ReadFile(filepath.Join("..", "..", "contracts", "TEST_ONLY.request-v2.snailreq"))
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan []byte, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			status, body := a.call(t, "POST", "/api/v1/admin/offline-requests", map[string]string{"requestFile": string(requestRaw)})
			if status != 200 {
				t.Errorf("request import %d %s", status, body)
			}
			results <- body
		}()
	}
	wg.Wait()
	close(results)
	replayed := 0
	for body := range results {
		if bytes.Contains(body, []byte(`"replayed":true`)) {
			replayed++
		}
	}
	if replayed != 1 {
		t.Fatal("concurrent request import was not idempotent")
	}
	changed := bytes.Replace(requestRaw, []byte("财务电脑"), []byte("另一台设备标签"), 1)
	// The fixture's label is read rather than assumed when constructing a conflict.
	var parsed map[string]any
	_ = json.Unmarshal(requestRaw, &parsed)
	parsed["deviceLabel"] = "同编号不同内容"
	changed, _ = json.Marshal(parsed)
	if status, body := a.call(t, "POST", "/api/v1/admin/offline-requests", map[string]string{"requestFile": string(changed)}); status != 409 || !bytes.Contains(body, []byte("REQUEST_CONFLICT")) {
		t.Fatalf("request conflict %d %s", status, body)
	}
	if status, body := a.call(t, "POST", "/api/v1/admin/offline-requests", map[string]string{"deviceCode": "STQ-WIN1-" + strings.Repeat("b", 64), "deviceLabel": "手工登记设备"}); status != 200 {
		t.Fatalf("full device code %d %s", status, body)
	}
	for _, bad := range []string{"STQ-WIN1-…", "STQ-WIN1-" + strings.Repeat("B", 64)} {
		if status, _ := a.call(t, "POST", "/api/v1/admin/offline-requests", map[string]string{"deviceCode": bad}); status != 400 {
			t.Fatal("invalid device code accepted")
		}
	}
	var requests, imports int
	_ = a.store.DB.NewRaw("SELECT count(*) FROM offline_requests").Scan(context.Background(), &requests)
	_ = a.store.DB.NewRaw("SELECT count(*) FROM audit_logs WHERE entity='offline-request'").Scan(context.Background(), &imports)
	if requests != 2 || imports != 2 {
		t.Fatalf("request rows=%d audits=%d", requests, imports)
	}
}
