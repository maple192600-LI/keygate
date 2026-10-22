package store

import (
	"context"
	"errors"
	"github.com/tabloy/keygate/internal/model"
	"github.com/uptrace/bun"
)

func (s *Store) CreateOfflineLicenseIn(ctx context.Context, tx bun.Tx, l *model.License) error {
	if l.FulfillmentMode != "offline_file" || l.CustomerID == "" || s.LicenseKeyAEAD == nil {
		return errors.New("offline license storage is not configured")
	}
	if err := s.prepareLicenseForInsert(l); err != nil {
		return err
	}
	_, err := tx.NewInsert().Model(l).Exec(ctx)
	return err
}
func (s *Store) IssuanceOperation(ctx context.Context, id string) (*model.LicenseIssuance, error) {
	result := new(model.LicenseIssuance)
	err := s.DB.NewSelect().Model(result).Where("operation_id = ?", id).Scan(ctx)
	return result, err
}
func (s *Store) LicenseIssuance(ctx context.Context, id string) (*model.LicenseIssuance, error) {
	result := new(model.LicenseIssuance)
	err := s.DB.NewSelect().Model(result).Where("id = ?", id).Scan(ctx)
	return result, err
}
