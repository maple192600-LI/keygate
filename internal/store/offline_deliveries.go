package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/tabloy/keygate/internal/offline"
	"github.com/uptrace/bun"
	"time"
)

type OfflineDelivery struct {
	bun.BaseModel `bun:"table:license_deliveries,alias:delivery"`
	ID            string    `bun:",pk" json:"id"`
	LicenseID     string    `json:"licenseId"`
	IssuanceID    *string   `json:"issuanceId"`
	OperationID   string    `json:"operationId"`
	OperationHash string    `json:"-"`
	Method        string    `json:"method"`
	Recipient     string    `json:"recipient"`
	DeliveredAt   time.Time `json:"deliveredAt"`
	Notes         string    `json:"notes"`
	CreatedBy     string    `json:"createdBy"`
	CreatedAt     time.Time `bun:",nullzero,default:now()" json:"createdAt"`
}

func (s *Store) RecordOfflineDelivery(ctx context.Context, row *OfflineDelivery) (*OfflineDelivery, bool, error) {
	var input any
	if row.IssuanceID != nil {
		input = struct{ IssuanceID, Method, Recipient, Notes, DeliveredAt string }{*row.IssuanceID, row.Method, row.Recipient, row.Notes, row.DeliveredAt.UTC().Format("2006-01-02T15:04:05.000Z")}
	} else {
		input = struct{ LicenseID, Method, Recipient, Notes, DeliveredAt string }{row.LicenseID, row.Method, row.Recipient, row.Notes, row.DeliveredAt.UTC().Format("2006-01-02T15:04:05.000Z")}
	}
	raw, _ := json.Marshal(input)
	sum := sha256.Sum256(raw)
	row.OperationHash = hex.EncodeToString(sum[:])
	var result *OfflineDelivery
	replayed := false
	err := RunInTx(ctx, s.DB, func(ctx context.Context, tx bun.Tx) error {
		if err := EnsureOfflineActor(ctx, tx, row.CreatedBy); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock(hashtextextended(?,0))", row.OperationID); err != nil {
			return err
		}
		prior := new(OfflineDelivery)
		err := tx.NewSelect().Model(prior).Where("operation_id=?", row.OperationID).Scan(ctx)
		if err == nil {
			if prior.OperationHash != row.OperationHash {
				return &offline.Error{Code: "OPERATION_CONFLICT", Message: "该操作号已登记不同交付内容"}
			}
			result = prior
			replayed = true
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if row.IssuanceID != nil {
			var licenseID string
			if err := tx.NewRaw("SELECT l.id FROM license_issuances i JOIN licenses l ON l.id=i.license_id WHERE i.id=? AND l.fulfillment_mode='offline_file'", *row.IssuanceID).Scan(ctx, &licenseID); err != nil {
				return err
			}
			if row.LicenseID != "" && row.LicenseID != licenseID {
				return errors.New("delivery issuance does not belong to license")
			}
			row.LicenseID = licenseID
		} else {
			var licenseID string
			if err := tx.NewRaw("SELECT id FROM licenses WHERE id=? AND fulfillment_mode='offline_file'", row.LicenseID).Scan(ctx, &licenseID); err != nil {
				return err
			}
		}
		row.ID = NewID()
		if _, err := tx.NewInsert().Model(row).Returning("*").Exec(ctx); err != nil {
			return err
		}
		if err := LocalAuditIn(ctx, tx, row.CreatedBy, "license-delivery", row.ID, "record", map[string]any{"issuanceId": row.IssuanceID, "licenseId": row.LicenseID}); err != nil {
			return err
		}
		result = row
		return nil
	})
	return result, replayed, err
}
func (s *Store) OfflineDeliveries(ctx context.Context, issuanceID string) ([]OfflineDelivery, error) {
	rows := []OfflineDelivery{}
	err := s.DB.NewSelect().Model(&rows).Where("issuance_id=?", issuanceID).OrderExpr("created_at DESC,id").Scan(ctx)
	return rows, err
}

func (s *Store) OfflineLicenseDeliveries(ctx context.Context, licenseID string) ([]OfflineDelivery, error) {
	var exists bool
	if err := s.DB.NewRaw("SELECT EXISTS(SELECT 1 FROM licenses WHERE id=? AND fulfillment_mode='offline_file')", licenseID).Scan(ctx, &exists); err != nil {
		return nil, err
	}
	if !exists {
		return nil, sql.ErrNoRows
	}
	rows := []OfflineDelivery{}
	err := s.DB.NewSelect().Model(&rows).Where("license_id=?", licenseID).OrderExpr("created_at DESC,id").Scan(ctx)
	return rows, err
}
