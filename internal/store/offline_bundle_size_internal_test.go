package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"github.com/tabloy/keygate/internal/offline"
	"strings"
	"testing"
)

func TestBusinessBundleExportRefusesUnreadableSize(t *testing.T) {
	// Exercise the serializer used after snapshot/contract validation. A real
	// bundle can grow by accumulating audit history; do not fill a database just
	// to prove the final byte boundary.
	b := &BusinessBundle{}
	header, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	b.CreatedAt = strings.Repeat("x", BundleLimit-len(header))
	raw, err := encodeBusinessBundle(b)
	if err != nil || len(raw) != BundleLimit {
		t.Fatal("exactly supported size rejected", len(raw), err)
	}
	if !bytes.Contains(raw, []byte(`"formatVersion"`)) {
		t.Fatal("not the actual bundle encoding")
	}
	b.CreatedAt += "x"
	raw, err = encodeBusinessBundle(b)
	var failure *offline.Error
	if raw != nil || !errors.As(err, &failure) || failure.Code != "BUNDLE_TOO_LARGE" {
		t.Fatal("oversized transfer was exported", err)
	}
}
