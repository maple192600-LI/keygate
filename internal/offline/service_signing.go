package offline

import (
	"encoding/hex"

	keycrypto "github.com/tabloy/keygate/internal/crypto"
)

func ServicePassphrase(master []byte) (string, error) {
	derived, err := keycrypto.DeriveSubkey(master, "snail-offline-license-signing-service/v1")
	if err != nil {
		return "", err
	}
	defer wipe(derived)
	return hex.EncodeToString(derived), nil
}
