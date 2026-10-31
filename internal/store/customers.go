package store

import (
	"context"
	"github.com/tabloy/keygate/internal/model"
	"github.com/uptrace/bun"
)

func (s *Store) CommercialCustomers(ctx context.Context, search string, page, size int) ([]model.CommercialCustomer, int, error) {
	out := []model.CommercialCustomer{}
	q := s.DB.NewSelect().Model(&out)
	if search != "" {
		term := "%" + search + "%"
		q.Where("(display_name ILIKE ? OR contact_name ILIKE ? OR contact_method ILIKE ? OR source_text ILIKE ? OR ('C-'||upper(replace(id,'-',''))) ILIKE ? OR EXISTS (SELECT 1 FROM licenses l WHERE l.fulfillment_mode='offline_file' AND l.customer_id=commercial_customer.id AND ('L-'||upper(replace(l.id,'-',''))) ILIKE ?))", term, term, term, term, term, term)
	}
	count, err := q.OrderExpr("created_at DESC, id").Limit(size).Offset((page - 1) * size).ScanAndCount(ctx)
	if err == nil && len(out) > 0 {
		ids := make([]string, len(out))
		for i, row := range out {
			ids[i] = row.ID
		}
		var counts []struct {
			CustomerID string
			Count      int
		}
		err = s.DB.NewSelect().TableExpr("licenses").ColumnExpr("customer_id,count(*) AS count").Where("fulfillment_mode='offline_file' AND customer_id IN (?)", bun.In(ids)).Group("customer_id").Scan(ctx, &counts)
		if err == nil {
			values := map[string]int{}
			for _, row := range counts {
				values[row.CustomerID] = row.Count
			}
			for i := range out {
				out[i].LicenseCount = values[out[i].ID]
			}
		}
	}
	return out, count, err
}
func (s *Store) CommercialCustomer(ctx context.Context, id string) (*model.CommercialCustomer, error) {
	c := new(model.CommercialCustomer)
	err := s.DB.NewSelect().Model(c).Where("id = ?", id).Scan(ctx)
	return c, err
}
func (s *Store) SaveCommercialCustomer(ctx context.Context, c *model.CommercialCustomer, actor string, create bool) error {
	return RunInTx(ctx, s.DB, func(ctx context.Context, tx bun.Tx) error {
		if create {
			if _, err := tx.NewInsert().Model(c).Exec(ctx); err != nil {
				return err
			}
		} else {
			if _, err := tx.NewUpdate().Model(c).Column("display_name", "customer_type", "contact_name", "contact_method", "email", "source_text", "notes").Set("updated_at=now()").WherePK().Exec(ctx); err != nil {
				return err
			}
		}
		action := "update"
		if create {
			action = "create"
		}
		return LocalAuditIn(ctx, tx, actor, "commercial-customer", c.ID, action, map[string]any{"displayName": c.DisplayName})
	})
}
