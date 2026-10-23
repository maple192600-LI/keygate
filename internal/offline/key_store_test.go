package offline

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"testing"
	"time"
)

func TestEncryptedSigningIdentityLifecycle(t *testing.T) {
	store := NewKeyStore(t.TempDir())
	password := "Synthetic-signing-passphrase-2026"
	before, err := store.Status()
	if err != nil || before.State != "uncreated" {
		t.Fatalf("initial %v %+v", err, before)
	}
	public, err := store.Create(password, time.Now().Add(time.Hour))
	if err != nil || public.State != "ready" {
		t.Fatalf("create %v", err)
	}
	defer store.Lock()
	raw, err := store.EncryptedIdentity()
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("PRIVATE KEY")) || bytes.Contains(raw, []byte(password)) {
		t.Fatal("secret leaked into container")
	}
	c, private, err := DecryptContainer(raw, password)
	if err != nil {
		t.Fatal(err)
	}
	defer wipe(private)
	if c.KeyID != public.KeyID || KeyID(private.Public().(ed25519.PublicKey)) != public.KeyID {
		t.Fatal("decrypted identity differs from public identity")
	}
	if _, _, err = DecryptContainer(raw, "Wrong-signing-passphrase-2026"); err == nil {
		t.Fatal("wrong signing password accepted")
	}
	original := append([]byte{}, raw...)
	if _, err = store.Create(password, time.Now().Add(time.Hour)); err == nil {
		t.Fatal("replaced existing identity")
	}
	p, err := Verify(fixture(t, "TEST_ONLY.license-v2.snaillic"), mustTestKeys(t), ProductID)
	if err != nil {
		t.Fatal(err)
	}
	file, signed, err := store.Sign(p)
	if err != nil {
		t.Fatal(err)
	}
	pub, err := ParsePublic([]byte(public.PublicKeyPEM))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = Verify(file, map[string]ed25519.PublicKey{public.KeyID: pub}, ProductID); err != nil || signed.KeyID != public.KeyID {
		t.Fatalf("independent signature: %v", err)
	}
	held := store.private
	store.Lock()
	for _, b := range held {
		if b != 0 {
			t.Fatal("active private handle not cleared")
		}
	}
	if _, _, err = store.Sign(p); err == nil {
		t.Fatal("locked identity signed")
	}
	if _, err = store.Unlock(password, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	store.deadline = time.Now().Add(-time.Second)
	if _, _, err = store.Sign(p); err == nil || len(store.private) != 0 {
		t.Fatal("expired signing lease signed")
	}
	after, err := store.EncryptedIdentity()
	if err != nil || !bytes.Equal(original, after) {
		t.Fatal("locking changed saved identity")
	}
	other := NewKeyStore(t.TempDir())
	if err = other.Import(original, public.KeyID); err != nil {
		t.Fatal(err)
	}
	defer other.Lock()
	if _, err = other.Unlock(password, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err = other.Import(original, "lic-"+string(bytes.Repeat([]byte("a"), 64))); err == nil {
		t.Fatal("history mismatch accepted")
	}
}
func TestContainerRejectsNonceAliasAndUnboundedKDF(t *testing.T) {
	c, private, err := encryptIdentity("Synthetic-container-passphrase")
	if err != nil {
		t.Fatal(err)
	}
	defer wipe(private)
	bad := c
	bad.NonceB64 = bad.CiphertextB64
	raw, _ := json.Marshal(bad)
	if _, err = ParseContainer(raw); err == nil {
		t.Fatal("nonce alias bypassed size validation")
	}
	bad = c
	bad.MemoryKiB = 999999999
	raw, _ = json.Marshal(bad)
	if _, err = ParseContainer(raw); err == nil {
		t.Fatal("unbounded KDF accepted")
	}
}
