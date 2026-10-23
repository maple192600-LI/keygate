package offline

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"testing"
	"time"
)

func TestRecoveryFixedCrossLanguageVector(t *testing.T) {
	var vector struct {
		Request     RecoveryRequest `json:"request"`
		Payload     RecoveryPayload `json:"payload"`
		RawRecovery string          `json:"rawRecovery"`
	}
	if err := json.Unmarshal(fixture(t, "TEST_ONLY.recovery.json"), &vector); err != nil {
		t.Fatal(err)
	}
	requestRaw, _ := json.Marshal(vector.Request)
	r, err := ParseRecoveryRequest(requestRaw)
	if err != nil {
		t.Fatal(err)
	}
	// Public RFC 8032 TEST 1 seed, never a commercial signing identity.
	seed, _ := hex.DecodeString("9d61b19deffd5a60ba844af492ec2cc44449c5697b326919703bac031cae7f60")
	keys := NewKeyStore(t.TempDir())
	keys.private = ed25519.NewKeyFromSeed(seed)
	keys.deadline = time.Now().Add(time.Hour)
	keys.sessionExpiry = keys.deadline
	defer keys.Lock()
	baseline, err := time.Parse(timestampLayout, vector.Payload.BaselineAt)
	if err != nil {
		t.Fatal(err)
	}
	raw, p, err := keys.SignRecovery(r, vector.Payload.RecoveryID, baseline)
	if err != nil || !bytes.Equal(raw, []byte(vector.RawRecovery)) || p != vector.Payload {
		t.Fatal("Go/Node recovery signing bytes differ", err)
	}
	if _, err = ParseRecoveryRequest(bytes.Replace(requestRaw, []byte("license-recovery-request"), []byte("license-recovery"), 1)); err == nil {
		t.Fatal("wrong request domain accepted")
	}
}
