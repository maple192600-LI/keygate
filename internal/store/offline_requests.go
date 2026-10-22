package store

import (
	"context"
	"errors"
	"github.com/tabloy/keygate/internal/model"
	"github.com/uptrace/bun"
)

var ErrOfflineRequestConflict = errors.New("REQUEST_CONFLICT")

func (s *Store) ImportOfflineRequest(ctx context.Context, request *model.OfflineRequest) (*model.OfflineRequest, bool, error) {
	var saved *model.OfflineRequest
	replayed := false
	err := RunInTx(ctx, s.DB, func(ctx context.Context, tx bun.Tx) error {
		if err := EnsureOfflineActor(ctx, tx, request.CreatedBy); err != nil {
			return err
		}
		result, err := tx.NewInsert().Model(request).On("CONFLICT(request_id) DO NOTHING").Exec(ctx)
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		if err != nil {
			return err
		}
		saved = new(model.OfflineRequest)
		if err = tx.NewSelect().Model(saved).Where("request_id = ?", request.RequestID).Scan(ctx); err != nil {
			return err
		}
		if saved.Fingerprint != request.Fingerprint {
			return ErrOfflineRequestConflict
		}
		replayed = count == 0
		if !replayed {
			return LocalAuditIn(ctx, tx, request.CreatedBy, "offline-request", request.RequestID, "import", nil)
		}
		return nil
	})
	return saved, replayed, err
}
