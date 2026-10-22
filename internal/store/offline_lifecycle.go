package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/tabloy/keygate/internal/model"
	"github.com/tabloy/keygate/internal/offline"
	"github.com/uptrace/bun"
	"time"
)

type OfflineLicenseView struct {
	ID                      string     `json:"id"`
	DisplayNumber           string     `json:"displayNumber"`
	CustomerID              string     `json:"customerId"`
	CustomerName            string     `json:"customerName"`
	ContactMethod           string     `json:"contactMethod"`
	SourceText              string     `json:"sourceText"`
	DeviceLabel             string     `json:"deviceLabel"`
	DeviceDigest            string     `json:"deviceDigest"`
	NotBefore               time.Time  `json:"notBefore"`
	ExpiresAt               *time.Time `json:"expiresAt"`
	Revision                int64      `json:"revision"`
	IssuanceID              string     `json:"issuanceId"`
	KeyID                   string     `json:"keyId"`
	FileName                string     `json:"fileName"`
	SHA256                  string     `bun:"sha256" json:"sha256"`
	TermState               string     `json:"termState"`
	DeliveryState           string     `json:"deliveryState"`
	IssuanceEnabled         bool       `json:"issuanceEnabled"`
	CustomerIssuanceEnabled bool       `json:"customerIssuanceEnabled"`
	UsageState              string     `json:"usageState"`
	DeliveryMethod          string     `json:"deliveryMethod"`
	DeliveryRecipient       string     `json:"deliveryRecipient"`
	DeliveredAt             *time.Time `json:"deliveredAt"`
}
type OfflineLicenseFilter struct {
	Expiring                                     bool
	Search, CustomerID, TermState, DeliveryState string
	Page, Size                                   int
}

const offlineTermSQL = "CASE WHEN i.not_before>now() THEN 'not_yet_valid' WHEN i.expires_at IS NULL THEN 'perpetual' WHEN i.expires_at<=now() THEN 'expired' ELSE 'valid' END"
const offlineDeliverySQL = "CASE WHEN EXISTS(SELECT 1 FROM license_deliveries d WHERE d.issuance_id=i.id) THEN 'recorded' ELSE 'pending' END"

func (s *Store) offlineLicenseQuery() *bun.SelectQuery {
	return s.DB.NewSelect().TableExpr("licenses l").ColumnExpr("l.id,l.display_number,c.id AS customer_id,c.display_name AS customer_name,c.contact_method,c.source_text,b.device_label,b.device_digest,i.not_before,i.expires_at,i.revision,i.id AS issuance_id,i.key_id,i.file_name,i.sha256,l.issuance_enabled,c.issuance_enabled AS customer_issuance_enabled, 'unreported' AS usage_state,COALESCE(d.method,'') AS delivery_method,COALESCE(d.recipient,'') AS delivery_recipient,d.delivered_at," + offlineTermSQL + " AS term_state," + offlineDeliverySQL + " AS delivery_state").Join("JOIN commercial_customers c ON c.id=l.customer_id").Join("JOIN license_issuances i ON i.id=l.current_issuance_id").Join("JOIN offline_device_bindings b ON b.id=i.binding_id").Join("LEFT JOIN LATERAL (SELECT method,recipient,delivered_at FROM license_deliveries WHERE issuance_id=i.id ORDER BY delivered_at DESC,created_at DESC,id DESC LIMIT 1) d ON true").Where("l.fulfillment_mode='offline_file'")
}
func (s *Store) OfflineLicenses(ctx context.Context, f OfflineLicenseFilter) ([]OfflineLicenseView, int, error) {
	rows := []OfflineLicenseView{}
	q := s.offlineLicenseQuery()
	if f.Expiring {
		q.Where("i.not_before<=now() AND i.expires_at>now() AND i.expires_at<=now()+interval '30 days'")
	}
	if f.Search != "" {
		term := "%" + f.Search + "%"
		q.Where("(c.display_name ILIKE ? OR l.display_number ILIKE ? OR c.contact_method ILIKE ? OR b.device_label ILIKE ? OR c.source_text ILIKE ?)", term, term, term, term, term)
	}
	if f.CustomerID != "" {
		q.Where("c.id=?", f.CustomerID)
	}
	if f.TermState != "" {
		q.Where("("+offlineTermSQL+")=?", f.TermState)
	}
	if f.DeliveryState != "" {
		q.Where("("+offlineDeliverySQL+")=?", f.DeliveryState)
	}
	count, err := q.OrderExpr("l.updated_at DESC,l.id").Limit(f.Size).Offset((f.Page-1)*f.Size).ScanAndCount(ctx, &rows)
	return rows, count, err
}
func (s *Store) OfflineLicense(ctx context.Context, id string) (*OfflineLicenseView, error) {
	row := new(OfflineLicenseView)
	err := s.offlineLicenseQuery().Where("l.id=?", id).Scan(ctx, row)
	return row, err
}
func (s *Store) OfflineLicenseHistory(ctx context.Context, id string) ([]model.LicenseIssuance, []model.OfflineDeviceBinding, []OfflineDelivery, error) {
	issuances := []model.LicenseIssuance{}
	bindings := []model.OfflineDeviceBinding{}
	deliveries := []OfflineDelivery{}
	err := s.DB.NewSelect().Model(&issuances).Where("license_id=?", id).OrderExpr("revision DESC").Scan(ctx)
	if err != nil {
		return nil, nil, nil, err
	}
	err = s.DB.NewSelect().Model(&bindings).Where("license_id=?", id).OrderExpr("registered_at DESC,id").Scan(ctx)
	if err != nil {
		return nil, nil, nil, err
	}
	err = s.DB.NewSelect().Model(&deliveries).Join("JOIN license_issuances i ON i.id=delivery.issuance_id").Where("i.license_id=?", id).OrderExpr("delivery.created_at DESC,delivery.id").Scan(ctx)
	return issuances, bindings, deliveries, err
}

type OfflineOverview struct {
	Customers       int    `json:"customers"`
	Licenses        int    `json:"licenses"`
	Valid           int    `json:"valid"`
	Expired         int    `json:"expired"`
	Expiring        int    `json:"expiring"`
	PendingDelivery int    `json:"pendingDelivery"`
	Stopped         int    `json:"stopped"`
	UsageState      string `json:"usageState"`
}

func (s *Store) OfflineOverview(ctx context.Context) (OfflineOverview, error) {
	var row OfflineOverview
	err := s.DB.NewRaw(`SELECT (SELECT count(*) FROM commercial_customers) AS customers,count(*) AS licenses,
 count(*) FILTER(WHERE i.not_before<=now() AND (i.expires_at IS NULL OR i.expires_at>now())) AS valid,
 count(*) FILTER(WHERE i.expires_at<=now()) AS expired,
 count(*) FILTER(WHERE i.not_before<=now() AND i.expires_at>now() AND i.expires_at<=now()+interval '30 days') AS expiring,
 count(*) FILTER(WHERE NOT EXISTS(SELECT 1 FROM license_deliveries d WHERE d.issuance_id=i.id)) AS pending_delivery,
 count(*) FILTER(WHERE NOT l.issuance_enabled OR NOT c.issuance_enabled) AS stopped,'unreported' AS usage_state
 FROM licenses l JOIN commercial_customers c ON c.id=l.customer_id JOIN license_issuances i ON i.id=l.current_issuance_id WHERE l.fulfillment_mode='offline_file'`).Scan(ctx, &row)
	return row, err
}

type OfflineStateCommand struct {
	OperationID      string `json:"operationId"`
	Enabled          bool   `json:"enabled"`
	ExpectedEnabled  *bool  `json:"expectedEnabled"`
	ExpectedRevision int64  `json:"expectedRevision"`
	Reason           string `json:"reason"`
}

func (s *Store) SetOfflineIssuanceState(ctx context.Context, actor, kind, id string, command OfflineStateCommand) (map[string]any, error) {
	input := struct {
		Kind, ID string
		Command  OfflineStateCommand
	}{kind, id, command}
	raw, _ := json.Marshal(input)
	sum := sha256.Sum256(raw)
	fingerprint := hex.EncodeToString(sum[:])
	var result map[string]any
	err := RunInTx(ctx, s.DB, func(ctx context.Context, tx bun.Tx) error {
		if _, err := tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock(hashtextextended(?,0))", "snail-state:"+command.OperationID); err != nil {
			return err
		}
		var prior struct {
			OperationHash string
			Result        json.RawMessage
		}
		err := tx.NewRaw("SELECT operation_hash,result FROM offline_management_operations WHERE operation_id=?", command.OperationID).Scan(ctx, &prior)
		if err == nil {
			if prior.OperationHash != fingerprint {
				return &offline.Error{Code: "OPERATION_CONFLICT", Message: "该操作号已用于其他管理条件"}
			}
			if err = json.Unmarshal(prior.Result, &result); err != nil {
				return err
			}
			result["replayed"] = true
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		table := "commercial_customers"
		where := "id=?"
		revision := int64(0)
		if kind == "license" {
			table = "licenses"
			where += " AND fulfillment_mode='offline_file'"
		} else if kind != "customer" {
			return errors.New("unsupported state target")
		}
		var enabled bool
		if kind == "license" {
			err = tx.NewRaw("SELECT issuance_enabled,current_revision FROM licenses WHERE "+where+" FOR UPDATE", id).Scan(ctx, &enabled, &revision)
		} else {
			err = tx.NewRaw("SELECT issuance_enabled FROM commercial_customers WHERE id=? FOR UPDATE", id).Scan(ctx, &enabled)
		}
		if err != nil {
			return err
		}
		if command.ExpectedEnabled == nil || enabled != *command.ExpectedEnabled || (kind == "license" && command.ExpectedRevision != revision) {
			return &offline.Error{Code: "REVISION_CONFLICT", Message: "管理状态或授权版本已变化，请刷新后核对"}
		}
		if _, err = tx.ExecContext(ctx, fmt.Sprintf("UPDATE %s SET issuance_enabled=?,updated_at=now() WHERE id=?", table), command.Enabled, id); err != nil {
			return err
		}
		result = map[string]any{"id": id, "enabled": command.Enabled, "replayed": false}
		encoded, _ := json.Marshal(result)
		if _, err = tx.ExecContext(ctx, "INSERT INTO offline_management_operations(operation_id,operation_hash,kind,result,created_by) VALUES(?,?,?,?,?)", command.OperationID, fingerprint, kind, json.RawMessage(encoded), actor); err != nil {
			return err
		}
		return LocalAuditIn(ctx, tx, actor, "offline-"+kind, id, "issuance-state", map[string]any{"enabled": command.Enabled, "reason": command.Reason})
	})
	return result, err
}
