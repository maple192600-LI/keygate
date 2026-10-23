package offline

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"
)

func TestBackupEncryptionPurposeAndStrictParameters(t *testing.T) {
	password := "Synthetic-backup-password"
	plain := []byte("synthetic database snapshot")
	a, err := SealBackup(plain, password)
	if err != nil {
		t.Fatal(err)
	}
	b, err := SealBackup(plain, password)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(a, b) {
		t.Fatal("salt/nonce reused")
	}
	opened, err := OpenBackup(a, password)
	if err != nil || !bytes.Equal(plain, opened) {
		t.Fatal("backup roundtrip", err)
	}
	for _, kind := range []string{"wrong-purpose", "cost", "unknown", "duplicate", "wrong-password"} {
		t.Run(kind, func(t *testing.T) {
			var value map[string]any
			_ = json.Unmarshal(a, &value)
			pass := password
			switch kind {
			case "wrong-purpose":
				value["kind"] = "snail-signing-identity"
			case "cost":
				value["memoryKiB"] = 2147483647
			case "unknown":
				value["privateKey"] = "forbidden"
			case "wrong-password":
				pass = "Different-synthetic-password"
			}
			raw, _ := json.Marshal(value)
			if kind == "duplicate" {
				raw = append([]byte(`{"formatVersion":1,`), raw[1:]...)
			}
			if _, err := OpenBackup(raw, pass); err == nil {
				t.Fatal("invalid backup accepted")
			}
		})
	}
}

func TestBackupAcceptedBoundaryCanBeOpened(t *testing.T) {
	// This was accepted by the former plain-text cutoff but produced an outer
	// file larger than the reader's 128 MiB limit. Check real bytes, not a mock.
	plain := bytes.Repeat([]byte("x"), BackupLimit*3/4-128)
	password := "Synthetic-boundary-backup-password"
	if raw, err := SealBackup(plain, password); err == nil || raw != nil {
		t.Fatal("unreadable boundary backup was exported")
	}
	raw, err := SealBackup(plain[:backupPlainLimit], password)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) > BackupLimit {
		t.Fatal("export exceeds restore limit", len(raw))
	}
	opened, err := OpenBackup(raw, password)
	if err != nil || !bytes.Equal(opened, plain[:backupPlainLimit]) {
		t.Fatal("accepted boundary cannot roundtrip", err)
	}
	var failure *Error
	_, err = SealBackup(plain, password)
	if !errors.As(err, &failure) || failure.Code != "BACKUP_TOO_LARGE" {
		t.Fatal("missing explicit size error", err)
	}
}
