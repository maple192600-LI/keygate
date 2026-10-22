package offline

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "contracts", name))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
func TestFixedPublicVector(t *testing.T) {
	public, err := ParsePublic(fixture(t, "TEST_ONLY.public-key.pem"))
	if err != nil {
		t.Fatal(err)
	}
	raw := fixture(t, "TEST_ONLY.license-v2.snaillic")
	p, err := Verify(raw, map[string]ed25519.PublicKey{KeyID(public): public}, ProductID)
	if err != nil {
		t.Fatal(err)
	}
	if p.Revision != 1 || p.KeyID != TestKeyID || p.ExpiresAt == nil || *p.ExpiresAt != "2026-11-22T01:00:00.000Z" {
		t.Fatalf("unexpected payload: %+v", p)
	}
	hash := sha256.Sum256(raw)
	if hex.EncodeToString(hash[:]) != "f00cf2cac79761b04c6ce0df466cd54d7f0826662dd4db8cf47a81b173be05e1" {
		t.Fatal("original file hash differs")
	}
	req, _, err := ParseRequest(fixture(t, "TEST_ONLY.request-v2.snailreq"), ProductID)
	if err != nil || req.Device.Digest != strings.Repeat("a", 64) {
		t.Fatalf("request: %v", err)
	}
}
func TestStrictJSONAndSignedFailures(t *testing.T) {
	for _, raw := range [][]byte{[]byte(`{"a":1,"\u0061":2}`), []byte(`{"a":"\ud800"}`), []byte(`{"a":"\udc00"}`), []byte(`{} {}`), []byte("\xef\xbb\xbf{}"), {0xff}, []byte(strings.Repeat("[", 10) + "0" + strings.Repeat("]", 10))} {
		if _, err := StrictJSON(raw, RequestLimit); err == nil {
			t.Fatalf("accepted malformed: %q", raw)
		}
	}
	if _, err := StrictJSON([]byte(`{"a":"\ud83d\ude00"}`), RequestLimit); err != nil {
		t.Fatalf("valid Unicode pair: %v", err)
	}
	public, private, _ := ed25519.GenerateKey(rand.Reader)
	p, _ := Verify(fixture(t, "TEST_ONLY.license-v2.snaillic"), mustTestKeys(t), ProductID)
	p.KeyID = KeyID(public)
	trusted := map[string]ed25519.PublicKey{p.KeyID: public}
	raw, err := Sign(p, private)
	if err != nil {
		t.Fatal(err)
	}
	var env Envelope
	_ = json.Unmarshal(raw, &env)
	payload, _ := canonicalBase64(env.PayloadB64)
	for _, bad := range [][]byte{
		bytes.Replace(payload, []byte(`"revision":1`), []byte(`"revision":1,"revision":2`), 1),
		bytes.Replace(payload, []byte(`2026-10-22`), []byte(`2026-02-30`), 1),
		bytes.Replace(payload, []byte(`"purpose":"commercial"`), []byte(`"purpose":"commercial","extra":1`), 1),
	} {
		env.PayloadB64 = encode64(bad)
		env.SignatureB64 = encode64(ed25519.Sign(private, append(append([]byte{}, signatureDomain...), bad...)))
		file, _ := compactJSON(env)
		if _, err := Verify(file, trusted, ProductID); err == nil {
			t.Fatal("accepted malformed signed payload")
		}
	}
	if _, err := Verify(raw, trusted, "another.product"); err == nil {
		t.Fatal("wrong product accepted")
	}
	if _, err := Verify(raw, map[string]ed25519.PublicKey{}, ProductID); err == nil {
		t.Fatal("unknown key accepted")
	}
}
func mustTestKeys(t *testing.T) map[string]ed25519.PublicKey {
	t.Helper()
	p, err := ParsePublic(fixture(t, "TEST_ONLY.public-key.pem"))
	if err != nil {
		t.Fatal(err)
	}
	return map[string]ed25519.PublicKey{KeyID(p): p}
}
func encode64(raw []byte) string { return base64.RawURLEncoding.EncodeToString(raw) }

func TestGoNodeBidirectional(t *testing.T) {
	module := os.Getenv("SNAIL_NODE_PROTOCOL")
	if module == "" {
		t.Skip("set SNAIL_NODE_PROTOCOL to the isolated client protocol-v2.cjs for cross-repository interoperability")
	}
	public, private, _ := ed25519.GenerateKey(rand.Reader)
	p, err := Verify(fixture(t, "TEST_ONLY.license-v2.snaillic"), mustTestKeys(t), ProductID)
	if err != nil {
		t.Fatal(err)
	}
	p.KeyID = KeyID(public)
	raw, err := Sign(p, private)
	if err != nil {
		t.Fatal(err)
	}
	input, _ := json.Marshal(map[string]any{"publicKey": string(PublicPEM(public)), "file": string(raw), "payload": p})
	script := `const fs=require('node:fs'),c=require('node:crypto'),p=require(process.argv[1]);const i=JSON.parse(fs.readFileSync(0,'utf8'));const go=p.verifyLicense(i.file,{[p.keyId(i.publicKey)]:i.publicKey});if(go.payload.revision!==1)throw Error('bad Go payload');const pair=c.generateKeyPairSync('ed25519');const payload={...i.payload,keyId:p.keyId(pair.publicKey)};const bytes=p.encodePayload(payload);const file=JSON.stringify({formatVersion:2,keyId:payload.keyId,payloadB64:bytes.toString('base64url'),signatureB64:c.sign(null,Buffer.concat([p.DOMAIN,bytes]),pair.privateKey).toString('base64url')});process.stdout.write(JSON.stringify({publicKey:pair.publicKey.export({type:'spki',format:'pem'}).toString(),file}));`
	cmd := exec.Command("node", "-e", script, module)
	cmd.Stdin = bytes.NewReader(input)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Node rejected Go: %v %s", err, out)
	}
	var result struct {
		PublicKey string `json:"publicKey"`
		File      string `json:"file"`
	}
	if err = json.Unmarshal(out, &result); err != nil {
		t.Fatal(err)
	}
	nodePublic, err := ParsePublic([]byte(result.PublicKey))
	if err != nil {
		t.Fatal(err)
	}
	got, err := Verify([]byte(result.File), map[string]ed25519.PublicKey{KeyID(nodePublic): nodePublic}, ProductID)
	if err != nil || got.LicenseID != p.LicenseID {
		t.Fatalf("Go rejected Node: %v", err)
	}
	requestRaw := fixture(t, "TEST_ONLY.request-v2.snailreq")
	var req Request
	_ = json.Unmarshal(requestRaw, &req)
	req.DeviceLabel = "合成\u2028设备\u2029😀"
	req.AppVersion = "1.0\u2028"
	requestRaw, _ = compactJSON(req)
	_, fingerprint, err := ParseRequest(requestRaw, ProductID)
	if err != nil {
		t.Fatal(err)
	}
	check := exec.Command("node", "-e", `const fs=require('node:fs'),p=require(process.argv[1]);process.stdout.write(p.request(fs.readFileSync(0)).fingerprint)`, module)
	check.Stdin = bytes.NewReader(requestRaw)
	nodeFingerprint, err := check.CombinedOutput()
	if err != nil || string(nodeFingerprint) != fingerprint {
		t.Fatalf("Unicode request fingerprints differ: Go %s Node %s %v", fingerprint, nodeFingerprint, err)
	}
}

func TestEquivalentJSONIntegers(t *testing.T) {
	keys := mustTestKeys(t)
	original := fixture(t, "TEST_ONLY.license-v2.snaillic")
	for _, text := range []string{"2.0", "2e0", "20e-1"} {
		raw := bytes.Replace(original, []byte(`"formatVersion":2`), []byte(`"formatVersion":`+text), 1)
		if _, err := Verify(raw, keys, ProductID); err != nil {
			t.Fatalf("integer-equivalent %s rejected: %v", text, err)
		}
	}
	public, private, _ := ed25519.GenerateKey(rand.Reader)
	p, _ := Verify(original, keys, ProductID)
	p.KeyID = KeyID(public)
	raw, _ := Sign(p, private)
	var env Envelope
	_ = json.Unmarshal(raw, &env)
	payload, _ := canonicalBase64(env.PayloadB64)
	for _, text := range []string{"1.0", "1e0", "1.0000000000000000001", "9007199254740991.1"} {
		changed := bytes.Replace(payload, []byte(`"revision":1`), []byte(`"revision":`+text), 1)
		env.PayloadB64 = encode64(changed)
		env.SignatureB64 = encode64(ed25519.Sign(private, append(append([]byte{}, signatureDomain...), changed...)))
		file, _ := compactJSON(env)
		_, err := Verify(file, map[string]ed25519.PublicKey{p.KeyID: public}, ProductID)
		valid := text == "1.0" || text == "1e0"
		if (err == nil) != valid {
			t.Fatalf("revision %s accepted=%v", text, err == nil)
		}
	}
}

func TestTermFixtures(t *testing.T) {
	var cases struct {
		Cases []struct {
			ID    string `json:"id"`
			Input struct {
				Action    string  `json:"action"`
				IssuedAt  string  `json:"issuedAt"`
				NotBefore string  `json:"notBefore"`
				Current   *Period `json:"current"`
				Term      struct {
					Kind  string          `json:"kind"`
					Value json.RawMessage `json:"value"`
				} `json:"term"`
			} `json:"input"`
			Expected TermResult `json:"expected"`
		} `json:"cases"`
		Assertions []struct {
			ID       string `json:"id"`
			CaseID   string `json:"caseId"`
			Now      string `json:"now"`
			Expected bool   `json:"expectedUsable"`
		} `json:"assertions"`
	}
	if err := json.Unmarshal(fixture(t, "term-cases.json"), &cases); err != nil {
		t.Fatal(err)
	}
	results := map[string]Period{}
	for _, tc := range cases.Cases {
		t.Run(tc.ID, func(t *testing.T) {
			term := Term{Kind: tc.Input.Term.Kind}
			switch term.Kind {
			case "permanent":
				term.Kind = "perpetual"
			case "date":
				_ = json.Unmarshal(tc.Input.Term.Value, &term.EndDate)
			default:
				_ = json.Unmarshal(tc.Input.Term.Value, &term.Amount)
			}
			now, _ := ParseUTC(tc.Input.IssuedAt)
			got, err := CalculateTerm(now, tc.Input.Action, term, tc.Input.Current)
			if err != nil {
				t.Fatal(err)
			}
			if got.NotBefore != tc.Expected.NotBefore || !equalOptional(got.ExpiresAt, tc.Expected.ExpiresAt) || (tc.Expected.Anchor != "" && got.Anchor != tc.Expected.Anchor) {
				t.Fatalf("got %+v, expected %+v", got, tc.Expected)
			}
			results[tc.ID] = got.Period
		})
	}
	for _, a := range cases.Assertions {
		t.Run(a.ID, func(t *testing.T) {
			now, err := ParseUTC(a.Now)
			if err != nil {
				t.Fatal(err)
			}
			if Usable(results[a.CaseID], now) != a.Expected {
				t.Fatal("exclusive boundary differs")
			}
		})
	}
}
func equalOptional(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}
func TestTermRejectionsAndFutureRenewal(t *testing.T) {
	now, _ := ParseUTC("2026-10-22T01:00:00.000Z")
	finite := "2026-11-22T01:00:00.000Z"
	current := &Period{"2026-11-01T01:00:00.000Z", &finite}
	got, err := CalculateTerm(now, "renew", Term{Kind: "days", Amount: 7}, current)
	if err != nil || got.NotBefore != current.NotBefore || *got.ExpiresAt != "2026-11-29T01:00:00.000Z" {
		t.Fatalf("future renewal %v %+v", err, got)
	}
	for _, term := range []Term{{Kind: "days", Amount: 0}, {Kind: "days", Amount: 36501}, {Kind: "months", Amount: 1201}, {Kind: "date", EndDate: "2026-02-30"}, {Kind: "date", EndDate: "10000-01-01"}, {Kind: "perpetual", Amount: 1}} {
		if _, err := CalculateTerm(now, "initial", term, nil); err == nil {
			t.Fatalf("accepted %+v", term)
		}
	}
	if _, err := CalculateTerm(now, "renew", Term{Kind: "date", EndDate: "2026-10-31"}, current); err == nil {
		t.Fatal("shortened renewal")
	}
	if _, err := CalculateTerm(now, "renew", Term{Kind: "days", Amount: 7}, &Period{NotBefore: now.Format(timestampLayout)}); err == nil {
		t.Fatal("renewed perpetual")
	}
	expired := now.Add(-time.Hour).Format(timestampLayout)
	if _, err := CalculateTerm(now, "transfer", Term{}, &Period{NotBefore: "2020-01-01T00:00:00.000Z", ExpiresAt: &expired}); err == nil {
		t.Fatal("transferred expired license")
	}
}
