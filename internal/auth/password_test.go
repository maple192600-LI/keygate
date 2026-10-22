package auth

import (
	"strings"
	"testing"
)

func TestPasswordHash(t *testing.T) {
	password := "合成管理密码20261023"
	a, err := HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	b, err := HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	if a == b || !VerifyPassword(a, password) || VerifyPassword(a, password+"x") {
		t.Fatal("salt or verification failed")
	}
	for _, bad := range []string{"short", strings.Repeat("a", 129), strings.Repeat("😀", 129)} {
		if ValidPassword(bad) {
			t.Fatal("accepted invalid length")
		}
	}
	if VerifyPassword(strings.Replace(a, "m=65536", "m=999999999", 1), password) {
		t.Fatal("unbounded parameters accepted")
	}
}
