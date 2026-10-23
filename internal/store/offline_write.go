package store

import (
	"context"
	"errors"
	"github.com/uptrace/bun"
)

// Only trusted file issuance/transfer/restore code calls this, on its own DB
// transaction. PostgreSQL clears the purpose on commit and on rollback.
func AllowOfflineWriteIn(ctx context.Context, tx bun.IDB, purpose string) error {
	if purpose != "issue" && purpose != "state" && purpose != "bundle" && purpose != "restore" {
		return errors.New("unsupported offline write purpose")
	}
	_, err := tx.ExecContext(ctx, "SELECT set_config('snail.offline_write',?,true)", purpose)
	return err
}
