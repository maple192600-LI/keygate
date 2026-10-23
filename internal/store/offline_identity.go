package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/tabloy/keygate/internal/offline"
)

// A public identity registered for client packaging constrains the signing
// identity even before the first issuance. A missing container is not a reset.
func (s *Store) ExpectedSigningIdentity(ctx context.Context, physical offline.PublicIdentity) (offline.PublicIdentity, error) {
	expected := physical
	invalid := func() (offline.PublicIdentity, error) {
		return offline.PublicIdentity{}, &offline.Error{Code: "SIGNING_IDENTITY_INVALID", Message: "签发公钥、身份文件或签发历史不一致，请保留资料并导入原身份"}
	}
	raw, err := s.GetSetting(ctx, "snail_public_identity")
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return expected, err
	}
	if err == nil {
		var saved offline.PublicIdentity
		if json.Unmarshal([]byte(raw), &saved) != nil || saved.KeyID == "" {
			return invalid()
		}
		public, e := offline.ParsePublic([]byte(saved.PublicKeyPEM))
		if e != nil || offline.KeyID(public) != saved.KeyID {
			return invalid()
		}
		if expected.KeyID != "" && expected.KeyID != saved.KeyID {
			return invalid()
		}
		expected = saved
	}
	var ids []string
	if err = s.DB.NewRaw("SELECT DISTINCT key_id FROM license_issuances").Scan(ctx, &ids); err != nil {
		return expected, err
	}
	if len(ids) > 1 || (len(ids) == 1 && expected.KeyID != "" && expected.KeyID != ids[0]) {
		return invalid()
	}
	if len(ids) == 1 && expected.KeyID == "" {
		expected.KeyID = ids[0]
	}
	if expected.KeyID != "" {
		expected.State = "public"
	}
	return expected, nil
}
