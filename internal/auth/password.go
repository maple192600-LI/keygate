package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"golang.org/x/crypto/argon2"
	"strings"
	"unicode/utf8"
)

const passwordPrefix = "$argon2id$v=19$m=65536,t=3,p=1$"

func ValidPassword(password string) bool {
	return utf8.ValidString(password) && utf8.RuneCountInString(password) >= 10 && utf8.RuneCountInString(password) <= 128 && len(password) <= 512
}
func HashPassword(password string) (string, error) {
	if !ValidPassword(password) {
		return "", errors.New("密码须为 10 至 128 个字符，UTF-8 不超过 512 字节")
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, 3, 64*1024, 1, 32)
	return passwordPrefix + base64.RawStdEncoding.EncodeToString(salt) + "$" + base64.RawStdEncoding.EncodeToString(key), nil
}
func VerifyPassword(encoded, password string) bool {
	if !ValidPassword(password) || !strings.HasPrefix(encoded, passwordPrefix) {
		return false
	}
	pieces := strings.Split(strings.TrimPrefix(encoded, passwordPrefix), "$")
	if len(pieces) != 2 {
		return false
	}
	salt, err := base64.RawStdEncoding.Strict().DecodeString(pieces[0])
	if err != nil || len(salt) != 16 {
		return false
	}
	expected, err := base64.RawStdEncoding.Strict().DecodeString(pieces[1])
	if err != nil || len(expected) != 32 {
		return false
	}
	key := argon2.IDKey([]byte(password), salt, 3, 64*1024, 1, 32)
	return subtle.ConstantTimeCompare(key, expected) == 1
}
