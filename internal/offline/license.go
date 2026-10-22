// Package offline implements the independent Snail v2 file contract.
package offline

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

const ProductID = "cn.snailfinance.desktop"
const MachineType = "windows-machineguid-sha256/v1"
const RequestLimit = 64 * 1024
const LicenseLimit = 128 * 1024
const TestKeyID = "lic-ef97ce129db1904c5744855fe725c724cae27e7b83b9ee017ec5e184df365cd2"
const timestampLayout = "2006-01-02T15:04:05.000Z"

var signatureDomain = []byte("stq-license/v2\x00")
var uuidPattern = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
var requestIDPattern = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
var digestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
var keyPattern = regexp.MustCompile(`^lic-[0-9a-f]{64}$`)

type Error struct {
	Code    string
	Message string
}

func (e *Error) Error() string     { return e.Message }
func invalid(message string) error { return &Error{"LICENSE_BAD_FORMAT", message} }

type Device struct {
	Type   string `json:"type"`
	Digest string `json:"digest"`
}
type Request struct {
	FormatVersion int    `json:"formatVersion"`
	RequestID     string `json:"requestId"`
	ProductID     string `json:"productId"`
	Device        Device `json:"device"`
	DeviceLabel   string `json:"deviceLabel"`
	AppVersion    string `json:"appVersion"`
}

// Field order is the signing contract. Do not use a map to encode a payload.
type Payload struct {
	FormatVersion int     `json:"formatVersion"`
	LicenseID     string  `json:"licenseId"`
	IssuanceID    string  `json:"issuanceId"`
	ProductID     string  `json:"productId"`
	Purpose       string  `json:"purpose"`
	CustomerID    string  `json:"customerId"`
	Revision      int64   `json:"revision"`
	IssuedAt      string  `json:"issuedAt"`
	NotBefore     string  `json:"notBefore"`
	ExpiresAt     *string `json:"expiresAt"`
	Binding       Device  `json:"binding"`
	KeyID         string  `json:"keyId"`
}
type Envelope struct {
	FormatVersion int    `json:"formatVersion"`
	KeyID         string `json:"keyId"`
	PayloadB64    string `json:"payloadB64"`
	SignatureB64  string `json:"signatureB64"`
}

// encoding/json replaces malformed UTF-8 and lone UTF-16 escapes. Reject both
// before decoding; escaped member names are compared after decoding below.
func validUnicode(raw []byte) bool {
	if !utf8.Valid(raw) {
		return false
	}
	quoted := false
	for i := 0; i < len(raw); i++ {
		if raw[i] == '"' {
			quoted = !quoted
			continue
		}
		if !quoted || raw[i] != '\\' {
			continue
		}
		i++
		if i >= len(raw) {
			return false
		}
		if raw[i] != 'u' {
			continue
		}
		if i+4 >= len(raw) {
			return false
		}
		a, err := hex.DecodeString(string(raw[i+1 : i+5]))
		if err != nil {
			return false
		}
		code := int(a[0])<<8 | int(a[1])
		i += 4
		if code >= 0xdc00 && code <= 0xdfff {
			return false
		}
		if code >= 0xd800 && code <= 0xdbff {
			if i+6 >= len(raw) || raw[i+1] != '\\' || raw[i+2] != 'u' {
				return false
			}
			b, err := hex.DecodeString(string(raw[i+3 : i+7]))
			if err != nil {
				return false
			}
			low := int(b[0])<<8 | int(b[1])
			if low < 0xdc00 || low > 0xdfff {
				return false
			}
			i += 6
		}
	}
	return true
}

// StrictJSON bounds the original bytes and nesting, rejects duplicate decoded
// keys, then returns data without allowing json.Unmarshal to erase evidence.
func StrictJSON(raw []byte, limit int) (any, error) {
	if len(raw) > limit || bytes.HasPrefix(raw, []byte{0xef, 0xbb, 0xbf}) || !validUnicode(raw) {
		return nil, invalid("文件大小或 UTF-8 编码无效")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var read func(int) (any, error)
	read = func(depth int) (any, error) {
		if depth > 8 {
			return nil, invalid("文件嵌套过深")
		}
		tok, err := d.Token()
		if err != nil {
			return nil, invalid("文件不是完整的 JSON")
		}
		delimiter, ok := tok.(json.Delim)
		if !ok {
			return tok, nil
		}
		switch delimiter {
		case '{':
			out := map[string]any{}
			for d.More() {
				key, err := d.Token()
				if err != nil {
					return nil, invalid("文件字段无效")
				}
				name, ok := key.(string)
				if !ok {
					return nil, invalid("文件字段无效")
				}
				if _, exists := out[name]; exists {
					return nil, invalid("文件包含重复字段")
				}
				val, err := read(depth + 1)
				if err != nil {
					return nil, err
				}
				out[name] = val
			}
			end, err := d.Token()
			if err != nil || end != json.Delim('}') {
				return nil, invalid("文件对象不完整")
			}
			return out, nil
		case '[':
			out := []any{}
			for d.More() {
				val, err := read(depth + 1)
				if err != nil {
					return nil, err
				}
				out = append(out, val)
			}
			end, err := d.Token()
			if err != nil || end != json.Delim(']') {
				return nil, invalid("文件数组不完整")
			}
			return out, nil
		default:
			return nil, invalid("文件结构无效")
		}
	}
	value, err := read(0)
	if err != nil {
		return nil, err
	}
	if _, err := d.Token(); err != io.EOF {
		return nil, invalid("文件末尾包含额外内容")
	}
	return value, nil
}
func exactFields(value any, names ...string) (map[string]any, error) {
	obj, ok := value.(map[string]any)
	if !ok || len(obj) != len(names) {
		return nil, invalid("文件字段不完整或包含未知字段")
	}
	for _, name := range names {
		if _, ok := obj[name]; !ok {
			return nil, invalid("文件缺少必要字段")
		}
	}
	return obj, nil
}
func decodeObject(raw []byte, limit int, target any, names ...string) (map[string]any, error) {
	data, err := StrictJSON(raw, limit)
	if err != nil {
		return nil, err
	}
	obj, err := exactFields(data, names...)
	if err != nil {
		return nil, err
	}
	// Also reject case-insensitive field aliases accepted by encoding/json.
	normalized, err := json.Marshal(obj)
	if err != nil {
		return nil, invalid("文件值无效")
	}
	if err = json.Unmarshal(normalized, target); err != nil {
		return nil, invalid("文件字段类型无效")
	}
	return obj, nil
}
func validateDevice(value any, device Device) error {
	if _, err := exactFields(value, "type", "digest"); err != nil {
		return err
	}
	if device.Type != MachineType || !digestPattern.MatchString(device.Digest) {
		return invalid("设备摘要无效")
	}
	return nil
}
func ParseUTC(value string) (time.Time, error) {
	t, err := time.Parse(timestampLayout, value)
	if err != nil || t.Format(timestampLayout) != value {
		return time.Time{}, invalid("授权时间必须是有效的 UTC 毫秒时间")
	}
	return t, nil
}
func ParseRequest(raw []byte, product string) (Request, string, error) {
	var req Request
	obj, err := decodeObject(raw, RequestLimit, &req, "formatVersion", "requestId", "productId", "device", "deviceLabel", "appVersion")
	if err != nil {
		return req, "", err
	}
	if req.FormatVersion != 2 || !requestIDPattern.MatchString(req.RequestID) || utf8.RuneCountInString(req.DeviceLabel) > 80 || utf8.RuneCountInString(req.AppVersion) > 40 {
		return req, "", invalid("申请版本、编号或描述无效")
	}
	for _, name := range []string{"deviceLabel", "appVersion"} {
		if _, ok := obj[name].(string); !ok {
			return req, "", invalid("申请描述必须是文本")
		}
	}
	if req.ProductID != product {
		return req, "", &Error{"LICENSE_WRONG_PRODUCT", "申请不属于当前产品"}
	}
	if err = validateDevice(obj["device"], req.Device); err != nil {
		return req, "", err
	}
	canonical, err := compactJSON(req)
	if err != nil {
		return req, "", err
	}
	sum := sha256.Sum256(canonical)
	return req, hex.EncodeToString(sum[:]), nil
}
func validatePayload(raw []byte, product string) (Payload, error) {
	var p Payload
	obj, err := decodeObject(raw, RequestLimit, &p, "formatVersion", "licenseId", "issuanceId", "productId", "purpose", "customerId", "revision", "issuedAt", "notBefore", "expiresAt", "binding", "keyId")
	if err != nil {
		return p, err
	}
	if p.FormatVersion != 2 || p.Purpose != "commercial" || !uuidPattern.MatchString(p.LicenseID) || !uuidPattern.MatchString(p.IssuanceID) || !uuidPattern.MatchString(p.CustomerID) || p.Revision < 1 || p.Revision > 9007199254740991 || !keyPattern.MatchString(p.KeyID) {
		return p, invalid("授权版本、编号或用途无效")
	}
	if p.ProductID != product {
		return p, &Error{"LICENSE_WRONG_PRODUCT", "授权不属于当前产品"}
	}
	if _, err = ParseUTC(p.IssuedAt); err != nil {
		return p, err
	}
	start, err := ParseUTC(p.NotBefore)
	if err != nil {
		return p, err
	}
	if p.ExpiresAt != nil {
		end, err := ParseUTC(*p.ExpiresAt)
		if err != nil {
			return p, err
		}
		if !end.After(start) {
			return p, invalid("截止时间必须晚于生效时间")
		}
	}
	if err = validateDevice(obj["binding"], p.Binding); err != nil {
		return p, err
	}
	return p, nil
}
func KeyID(public ed25519.PublicKey) string {
	der, err := x509.MarshalPKIXPublicKey(public)
	if err != nil {
		return ""
	}
	h := sha256.New()
	h.Write([]byte("stq-license-key/v1\x00"))
	h.Write(der)
	return "lic-" + hex.EncodeToString(h.Sum(nil))
}
func PublicPEM(public ed25519.PublicKey) []byte {
	der, _ := x509.MarshalPKIXPublicKey(public)
	return pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})
}
func ParsePublic(raw []byte) (ed25519.PublicKey, error) {
	block, rest := pem.Decode(raw)
	if block == nil || block.Type != "PUBLIC KEY" || len(bytes.TrimSpace(rest)) != 0 {
		return nil, invalid("公钥文件无效")
	}
	key, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, invalid("公钥文件无效")
	}
	public, ok := key.(ed25519.PublicKey)
	if !ok {
		return nil, invalid("公钥必须使用 Ed25519")
	}
	return public, nil
}
func canonicalBase64(value string) ([]byte, error) {
	if value == "" || strings.ContainsAny(value, "=\r\n") {
		return nil, invalid("授权编码无效")
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(value)
	if err != nil || base64.RawURLEncoding.EncodeToString(raw) != value {
		return nil, invalid("授权编码不规范")
	}
	return raw, nil
}
func Verify(raw []byte, trusted map[string]ed25519.PublicKey, product string) (Payload, error) {
	var env Envelope
	_, err := decodeObject(raw, LicenseLimit, &env, "formatVersion", "keyId", "payloadB64", "signatureB64")
	if err != nil {
		return Payload{}, err
	}
	if env.FormatVersion != 2 || !keyPattern.MatchString(env.KeyID) {
		return Payload{}, invalid("授权格式版本或身份无效")
	}
	public, ok := trusted[env.KeyID]
	if !ok || len(public) != ed25519.PublicKeySize || KeyID(public) != env.KeyID {
		return Payload{}, &Error{"LICENSE_UNKNOWN_KEY", "当前版本不信任此签发身份"}
	}
	payload, err := canonicalBase64(env.PayloadB64)
	if err != nil {
		return Payload{}, err
	}
	signature, err := canonicalBase64(env.SignatureB64)
	if err != nil {
		return Payload{}, err
	}
	message := append(append([]byte{}, signatureDomain...), payload...)
	if len(signature) != ed25519.SignatureSize || !ed25519.Verify(public, message, signature) {
		return Payload{}, &Error{"LICENSE_INVALID_SIGNATURE", "授权签名无效"}
	}
	p, err := validatePayload(payload, product)
	if err != nil {
		return p, err
	}
	if p.KeyID != env.KeyID {
		return p, invalid("内外签发身份不一致")
	}
	return p, nil
}
func compactJSON(value any) ([]byte, error) {
	var out bytes.Buffer
	e := json.NewEncoder(&out)
	e.SetEscapeHTML(false)
	if err := e.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(out.Bytes(), []byte{'\n'}), nil
}
func Sign(p Payload, private ed25519.PrivateKey) ([]byte, error) {
	if len(private) != ed25519.PrivateKeySize {
		return nil, invalid("签发身份未就绪")
	}
	if p.KeyID != KeyID(private.Public().(ed25519.PublicKey)) {
		return nil, invalid("签发身份与载荷不一致")
	}
	raw, err := compactJSON(p)
	if err != nil {
		return nil, err
	}
	if _, err = validatePayload(raw, ProductID); err != nil {
		return nil, err
	}
	sig := ed25519.Sign(private, append(append([]byte{}, signatureDomain...), raw...))
	file, err := compactJSON(Envelope{2, p.KeyID, base64.RawURLEncoding.EncodeToString(raw), base64.RawURLEncoding.EncodeToString(sig)})
	if err != nil {
		return nil, err
	}
	if _, err = Verify(file, map[string]ed25519.PublicKey{p.KeyID: private.Public().(ed25519.PublicKey)}, ProductID); err != nil {
		return nil, fmt.Errorf("签发后独立核对失败: %w", err)
	}
	return file, nil
}
