package offline

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"time"
)

type RecoveryRequest struct {
	FormatVersion    int    `json:"formatVersion"`
	Kind             string `json:"kind"`
	ProductID        string `json:"productId"`
	RequestID        string `json:"requestId"`
	InstallationID   string `json:"installationId"`
	Challenge        string `json:"challenge"`
	Device           Device `json:"device"`
	StateFingerprint string `json:"stateFingerprint"`
	AppVersion       string `json:"appVersion"`
	ProtectedWallMs  int64  `json:"protectedWallMs"`
}
type RecoveryPayload struct {
	FormatVersion    int    `json:"formatVersion"`
	Kind             string `json:"kind"`
	ProductID        string `json:"productId"`
	RecoveryID       string `json:"recoveryId"`
	RequestID        string `json:"requestId"`
	InstallationID   string `json:"installationId"`
	Challenge        string `json:"challenge"`
	Binding          Device `json:"binding"`
	StateFingerprint string `json:"stateFingerprint"`
	BaselineAt       string `json:"baselineAt"`
	ExpiresAt        string `json:"expiresAt"`
	KeyID            string `json:"keyId"`
}

func ParseRecoveryRequest(raw []byte) (RecoveryRequest, error) {
	var r RecoveryRequest
	_, err := decodeObject(raw, RequestLimit, &r, "formatVersion", "kind", "productId", "requestId", "installationId", "challenge", "device", "stateFingerprint", "appVersion", "protectedWallMs")
	if err != nil {
		return r, err
	}
	if r.FormatVersion != 1 || r.Kind != "license-recovery-request" || r.ProductID != ProductID ||
		!ValidUUID4(r.RequestID) || !ValidUUID4(r.InstallationID) || !ValidUUID4(r.Challenge) ||
		r.Device.Type != MachineType || !digestPattern.MatchString(r.Device.Digest) || !digestPattern.MatchString(r.StateFingerprint) ||
		len(r.AppVersion) > 40 || r.ProtectedWallMs < 0 || r.ProtectedWallMs > 9007199254740991 {
		return r, invalid("修复申请格式无效")
	}
	return r, nil
}
func (s *KeyStore) SignRecovery(r RecoveryRequest, recoveryID string, now time.Time) ([]byte, RecoveryPayload, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.private) == 0 || (!s.serviceMode && !time.Now().Before(s.deadline)) {
		s.locked()
		return nil, RecoveryPayload{}, &Error{"SIGNING_KEY_LOCKED", "请输入签发密码解锁"}
	}
	p := RecoveryPayload{1, "license-recovery", ProductID, recoveryID, r.RequestID, r.InstallationID, r.Challenge, r.Device, r.StateFingerprint,
		now.UTC().Format(timestampLayout), now.Add(7 * 24 * time.Hour).UTC().Format(timestampLayout), KeyID(s.private.Public().(ed25519.PublicKey))}
	bytes, err := json.Marshal(p)
	if err != nil {
		return nil, p, err
	}
	signature := ed25519.Sign(s.private, append([]byte("stq-license-recovery/v1\x00"), bytes...))
	envelope := struct {
		FormatVersion int    `json:"formatVersion"`
		Kind          string `json:"kind"`
		KeyID         string `json:"keyId"`
		PayloadB64    string `json:"payloadB64"`
		SignatureB64  string `json:"signatureB64"`
	}{1, "license-recovery", p.KeyID, base64.RawURLEncoding.EncodeToString(bytes), base64.RawURLEncoding.EncodeToString(signature)}
	raw, err := json.Marshal(envelope)
	if err == nil {
		if !s.serviceMode {
			s.arm()
		}
	}
	return raw, p, err
}
