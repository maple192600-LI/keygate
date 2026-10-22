package offline

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"github.com/tabloy/keygate/internal/auth"
	"golang.org/x/crypto/argon2"
)

// The backup password is independent of the signing password. Fixed parameters
// are checked before allocating KDF memory; no archive controls its own cost.
type BackupContainer struct {
	FormatVersion int    `json:"formatVersion"`
	Kind          string `json:"kind"`
	KDF           string `json:"kdf"`
	MemoryKiB     int    `json:"memoryKiB"`
	Iterations    int    `json:"iterations"`
	Parallelism   int    `json:"parallelism"`
	SaltB64       string `json:"saltB64"`
	NonceB64      string `json:"nonceB64"`
	CiphertextB64 string `json:"ciphertextB64"`
}

const BackupLimit = 128 * 1024 * 1024

// Reserve room for the AEAD tag, base64 expansion and the complete JSON header.
const backupPlainLimit = BackupLimit*3/4 - 1024

var backupAAD = []byte("snail-full-backup/v1\x00")

func backupError() error {
	return &Error{Code: "BACKUP_INVALID", Message: "备份密码不正确、文件损坏或版本不受支持；当前资料保留"}
}
func SealBackup(plain []byte, password string) ([]byte, error) {
	if !auth.ValidPassword(password) {
		return nil, &Error{Code: "BACKUP_PASSWORD_INVALID", Message: "备份密码须为 10 至 128 个字符"}
	}
	if len(plain) > backupPlainLimit {
		return nil, &Error{Code: "BACKUP_TOO_LARGE", Message: "备份超过本期本地文件大小限制"}
	}
	salt, nonce := make([]byte, 16), make([]byte, 12)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	key := argon2.IDKey([]byte(password), salt, 3, 65536, 1, 32)
	defer wipe(key)
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	c := BackupContainer{1, "snail-full-backup", "argon2id/v19", 65536, 3, 1, base64.RawURLEncoding.EncodeToString(salt), base64.RawURLEncoding.EncodeToString(nonce), base64.RawURLEncoding.EncodeToString(aead.Seal(nil, nonce, plain, backupAAD))}
	raw, err := json.Marshal(c)
	if err != nil {
		return nil, err
	}
	if len(raw) > BackupLimit {
		return nil, &Error{Code: "BACKUP_TOO_LARGE", Message: "备份超过本期本地文件大小限制"}
	}
	return raw, nil
}
func OpenBackup(raw []byte, password string) ([]byte, error) {
	var c BackupContainer
	_, err := decodeObject(raw, BackupLimit, &c, "formatVersion", "kind", "kdf", "memoryKiB", "iterations", "parallelism", "saltB64", "nonceB64", "ciphertextB64")
	if err != nil || !auth.ValidPassword(password) || c.FormatVersion != 1 || c.Kind != "snail-full-backup" || c.KDF != "argon2id/v19" || c.MemoryKiB != 65536 || c.Iterations != 3 || c.Parallelism != 1 {
		return nil, backupError()
	}
	salt, e := canonicalBase64(c.SaltB64)
	nonce, f := canonicalBase64(c.NonceB64)
	ct, g := canonicalBase64(c.CiphertextB64)
	if e != nil || f != nil || g != nil || len(salt) != 16 || len(nonce) != 12 || len(ct) < 16 {
		return nil, backupError()
	}
	key := argon2.IDKey([]byte(password), salt, 3, 65536, 1, 32)
	defer wipe(key)
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	plain, err := aead.Open(nil, nonce, ct, backupAAD)
	if err != nil {
		return nil, backupError()
	}
	return plain, nil
}
