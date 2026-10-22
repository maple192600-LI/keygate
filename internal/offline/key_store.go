package offline

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"github.com/tabloy/keygate/internal/auth"
	"golang.org/x/crypto/argon2"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type KeyContainer struct {
	FormatVersion int    `json:"formatVersion"`
	KeyID         string `json:"keyId"`
	PublicKeyPEM  string `json:"publicKeyPem"`
	KDF           string `json:"kdf"`
	MemoryKiB     int    `json:"memoryKiB"`
	Iterations    int    `json:"iterations"`
	Parallelism   int    `json:"parallelism"`
	SaltB64       string `json:"saltB64"`
	NonceB64      string `json:"nonceB64"`
	CiphertextB64 string `json:"ciphertextB64"`
	CreatedAt     string `json:"createdAt"`
}
type PublicIdentity struct {
	KeyID        string `json:"keyId"`
	PublicKeyPEM string `json:"publicKeyPem"`
	State        string `json:"state"`
	CreatedAt    string `json:"createdAt"`
}
type KeyStore struct {
	mu            sync.Mutex
	root          string
	private       ed25519.PrivateKey
	sessionExpiry time.Time
	deadline      time.Time
	timer         *time.Timer
	generation    uint64
}

func NewKeyStore(root string) *KeyStore  { return &KeyStore{root: root} }
func identityError(message string) error { return &Error{"SIGNING_IDENTITY_INVALID", message} }
func wipe(raw []byte) {
	for i := range raw {
		raw[i] = 0
	}
}
func identityAAD(c KeyContainer) []byte {
	return []byte("snail-signing-identity/v1\x00" + c.KeyID + "\x00" + c.PublicKeyPEM)
}
func ParseContainer(raw []byte) (KeyContainer, error) {
	var c KeyContainer
	_, err := decodeObject(raw, RequestLimit, &c, "formatVersion", "keyId", "publicKeyPem", "kdf", "memoryKiB", "iterations", "parallelism", "saltB64", "nonceB64", "ciphertextB64", "createdAt")
	if err != nil {
		return c, err
	}
	if c.FormatVersion != 1 || c.KDF != "argon2id/v19" || c.MemoryKiB != 65536 || c.Iterations != 3 || c.Parallelism != 1 {
		return c, identityError("签发身份容器版本或参数不受支持")
	}
	public, err := ParsePublic([]byte(c.PublicKeyPEM))
	if err != nil || c.KeyID != KeyID(public) || !bytes.Equal(PublicPEM(public), []byte(c.PublicKeyPEM)) {
		return c, identityError("签发公钥与身份编号不一致")
	}
	if c.KeyID == TestKeyID {
		return c, identityError("公开测试身份不能用作正式签发身份")
	}
	if _, err = ParseUTC(c.CreatedAt); err != nil {
		return c, err
	}
	for _, field := range []struct {
		value string
		size  int
	}{{c.SaltB64, 16}, {c.NonceB64, 12}, {c.CiphertextB64, 48}} {
		raw, err := canonicalBase64(field.value)
		if err != nil || len(raw) != field.size {
			return c, identityError("签发身份加密内容无效")
		}
	}
	return c, nil
}
func DecryptContainer(raw []byte, passphrase string) (KeyContainer, ed25519.PrivateKey, error) {
	c, err := ParseContainer(raw)
	if err != nil {
		return c, nil, err
	}
	if !auth.ValidPassword(passphrase) {
		return c, nil, identityError("签发密码不正确或身份文件损坏")
	}
	salt, _ := canonicalBase64(c.SaltB64)
	nonce, _ := canonicalBase64(c.NonceB64)
	ciphertext, _ := canonicalBase64(c.CiphertextB64)
	key := argon2.IDKey([]byte(passphrase), salt, 3, 65536, 1, 32)
	defer wipe(key)
	block, err := aes.NewCipher(key)
	if err != nil {
		return c, nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return c, nil, err
	}
	seed, err := aead.Open(nil, nonce, ciphertext, identityAAD(c))
	if err != nil {
		return c, nil, identityError("签发密码不正确或身份文件损坏")
	}
	defer wipe(seed)
	private := ed25519.NewKeyFromSeed(seed)
	if KeyID(private.Public().(ed25519.PublicKey)) != c.KeyID {
		wipe(private)
		return c, nil, identityError("签发私钥与公开身份不一致")
	}
	return c, private, nil
}
func encryptIdentity(passphrase string) (KeyContainer, ed25519.PrivateKey, error) {
	if !auth.ValidPassword(passphrase) {
		return KeyContainer{}, nil, identityError("签发密码须为 10 至 128 个字符")
	}
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return KeyContainer{}, nil, err
	}
	salt := make([]byte, 16)
	nonce := make([]byte, 12)
	if _, err = rand.Read(salt); err != nil {
		wipe(private)
		return KeyContainer{}, nil, err
	}
	if _, err = rand.Read(nonce); err != nil {
		wipe(private)
		return KeyContainer{}, nil, err
	}
	c := KeyContainer{FormatVersion: 1, KeyID: KeyID(public), PublicKeyPEM: string(PublicPEM(public)), KDF: "argon2id/v19", MemoryKiB: 65536, Iterations: 3, Parallelism: 1, SaltB64: base64.RawURLEncoding.EncodeToString(salt), NonceB64: base64.RawURLEncoding.EncodeToString(nonce), CreatedAt: time.Now().UTC().Format(timestampLayout)}
	key := argon2.IDKey([]byte(passphrase), salt, 3, 65536, 1, 32)
	defer wipe(key)
	block, err := aes.NewCipher(key)
	if err != nil {
		wipe(private)
		return c, nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		wipe(private)
		return c, nil, err
	}
	seed := private.Seed()
	defer wipe(seed)
	c.CiphertextB64 = base64.RawURLEncoding.EncodeToString(aead.Seal(nil, nonce, seed, identityAAD(c)))
	return c, private, nil
}
func (s *KeyStore) file(create bool) (string, error) {
	if s.root == "" {
		return "", identityError("请通过本机管理客户端打开签发身份")
	}
	root, err := filepath.Abs(s.root)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil || !strings.EqualFold(resolved, root) {
		return "", identityError("签发资料根目录不是安全的本机目录")
	}
	directory := filepath.Join(root, "keys")
	stat, err := os.Lstat(directory)
	if errors.Is(err, os.ErrNotExist) && create {
		err = os.Mkdir(directory, 0700)
		if err == nil {
			stat, err = os.Lstat(directory)
		}
	}
	if errors.Is(err, os.ErrNotExist) && !create {
		return filepath.Join(directory, "signing-identity.json"), nil
	}
	if err != nil {
		return "", err
	}
	if !stat.IsDir() || stat.Mode()&os.ModeSymlink != 0 {
		return "", identityError("签发身份目录不安全")
	}
	resolved, err = filepath.EvalSymlinks(directory)
	if err != nil || !strings.EqualFold(resolved, directory) {
		return "", identityError("签发身份目录不安全")
	}
	file := filepath.Join(directory, "signing-identity.json")
	if stat, err = os.Lstat(file); err == nil {
		if !stat.Mode().IsRegular() || stat.Mode()&os.ModeSymlink != 0 || stat.Size() > RequestLimit {
			return "", identityError("签发身份文件不安全或过大")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	return file, nil
}
func (s *KeyStore) read() (KeyContainer, []byte, error) {
	file, err := s.file(false)
	if err != nil {
		return KeyContainer{}, nil, err
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		return KeyContainer{}, nil, err
	}
	c, err := ParseContainer(raw)
	return c, raw, err
}
func (s *KeyStore) Status() (PublicIdentity, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, _, err := s.read()
	if errors.Is(err, os.ErrNotExist) {
		return PublicIdentity{State: "uncreated"}, nil
	}
	if err != nil {
		return PublicIdentity{}, err
	}
	state := "locked"
	if len(s.private) > 0 && time.Now().Before(s.deadline) {
		state = "ready"
	} else {
		s.locked()
	}
	return PublicIdentity{c.KeyID, c.PublicKeyPEM, state, c.CreatedAt}, nil
}
func (s *KeyStore) write(raw []byte, replace bool) (failure error) {
	file, err := s.file(true)
	if err != nil {
		return err
	}
	if !replace {
		if _, err = os.Lstat(file); err == nil {
			return identityError("签发身份已经存在，请勿重复创建")
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	temporary := file + ".tmp-" + hexRandom()
	fd, err := os.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	published := false
	defer func() {
		if !published {
			if err := os.Remove(temporary); err != nil && !errors.Is(err, os.ErrNotExist) {
				failure = errors.Join(failure, identityError("签发身份临时文件未能清理，请保留原资料并检查目录"))
			}
		}
	}()
	if _, err = fd.Write(raw); err == nil {
		err = fd.Sync()
	}
	closeErr := fd.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = os.Rename(temporary, file); err != nil {
		return err
	}
	published = true
	return nil
}
func hexRandom() string {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}
func (s *KeyStore) Create(passphrase string, sessionExpiry time.Time) (PublicIdentity, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, private, err := encryptIdentity(passphrase)
	if err != nil {
		return PublicIdentity{}, err
	}
	raw, _ := json.Marshal(c)
	if err = s.write(raw, false); err != nil {
		wipe(private)
		return PublicIdentity{}, err
	}
	s.locked()
	s.private = private
	s.sessionExpiry = sessionExpiry
	s.arm()
	return PublicIdentity{c.KeyID, c.PublicKeyPEM, "ready", c.CreatedAt}, nil
}
func (s *KeyStore) Unlock(passphrase string, sessionExpiry time.Time) (PublicIdentity, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, raw, err := s.read()
	if err != nil {
		return PublicIdentity{}, err
	}
	c, private, err := DecryptContainer(raw, passphrase)
	if err != nil {
		return PublicIdentity{}, err
	}
	s.locked()
	s.private = private
	s.sessionExpiry = sessionExpiry
	s.arm()
	return PublicIdentity{c.KeyID, c.PublicKeyPEM, "ready", c.CreatedAt}, nil
}
func (s *KeyStore) locked() {
	wipe(s.private)
	s.private = nil
	s.generation++
	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}
}
func (s *KeyStore) Lock() { s.mu.Lock(); defer s.mu.Unlock(); s.locked() }
func (s *KeyStore) arm() {
	s.generation++
	generation := s.generation
	if s.timer != nil {
		s.timer.Stop()
	}
	deadline := time.Now().Add(15 * time.Minute)
	if s.sessionExpiry.Before(deadline) {
		deadline = s.sessionExpiry
	}
	s.deadline = deadline
	s.timer = time.AfterFunc(time.Until(deadline), func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.generation == generation {
			s.locked()
		}
	})
}
func (s *KeyStore) Sign(p Payload) ([]byte, Payload, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.private) == 0 || !time.Now().Before(s.deadline) {
		s.locked()
		return nil, p, &Error{"SIGNING_KEY_LOCKED", "请输入签发密码解锁"}
	}
	p.KeyID = KeyID(s.private.Public().(ed25519.PublicKey))
	raw, err := Sign(p, s.private)
	if err == nil {
		s.arm()
	}
	return raw, p, err
}
func (s *KeyStore) EncryptedIdentity() ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, raw, err := s.read()
	return raw, err
}
func (s *KeyStore) Import(raw []byte, expectedKeyID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := ParseContainer(raw)
	if err != nil {
		return err
	}
	if expectedKeyID != "" && expectedKeyID != c.KeyID {
		return identityError("导入身份与已有签发历史不一致")
	}
	if current, _, err := s.read(); err == nil && current.KeyID != c.KeyID {
		return identityError("不能覆盖为另一签发身份")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err = s.write(raw, true); err != nil {
		return err
	}
	s.locked()
	return nil
}
