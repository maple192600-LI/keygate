package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/tabloy/keygate/internal/model"
	"github.com/tabloy/keygate/internal/offline"
	"github.com/uptrace/bun"
)

type OfflineLicenseView struct {
	ID                      string              `json:"id"`
	CustomerNumber          string              `json:"customerNumber"`
	LicenseNumber           string              `json:"licenseNumber"`
	DisplayNumber           string              `json:"displayNumber"`
	CustomerID              string              `json:"customerId"`
	CustomerName            string              `json:"customerName"`
	ContactMethod           string              `json:"contactMethod"`
	SourceText              string              `json:"sourceText"`
	DeviceLimit             int                 `json:"deviceLimit"`
	Term                    OfflineTermView     `json:"term"`
	ActivationAnchor        string              `json:"activationAnchor"`
	TermKind                string              `bun:"term_kind" json:"-"`
	TermAmount              int                 `bun:"term_amount" json:"-"`
	TermEndDate             *time.Time          `bun:"term_end_date" json:"-"`
	TermStartsAt            *time.Time          `bun:"term_starts_at" json:"-"`
	Devices                 []OfflineDeviceView `json:"devices"`
	NotBefore               *time.Time          `json:"notBefore"`
	ExpiresAt               *time.Time          `json:"expiresAt"`
	Revision                int64               `json:"revision"`
	IssuanceID              string              `json:"issuanceId"`
	KeyID                   string              `json:"keyId"`
	FileName                string              `json:"fileName"`
	SHA256                  string              `bun:"sha256" json:"sha256"`
	TermState               string              `json:"termState"`
	DeliveryState           string              `json:"deliveryState"`
	IssuanceEnabled         bool                `json:"issuanceEnabled"`
	CustomerIssuanceEnabled bool                `json:"customerIssuanceEnabled"`
	UsageState              string              `json:"usageState"`
	DeliveryMethod          string              `json:"deliveryMethod"`
	DeliveryRecipient       string              `json:"deliveryRecipient"`
	DeliveredAt             *time.Time          `json:"deliveredAt"`
}

type OfflineTermView struct {
	Kind     string     `json:"kind"`
	Amount   int        `json:"amount,omitempty"`
	EndDate  *string    `json:"endDate,omitempty"`
	StartsAt *time.Time `json:"startsAt,omitempty"`
}

type OfflineDeviceView struct {
	BindingID      string     `json:"bindingId"`
	DeviceLabel    string     `json:"deviceLabel"`
	DeviceState    string     `json:"deviceState"`
	AppVersion     string     `json:"appVersion"`
	BuildID        string     `json:"buildId"`
	LastReportedAt *time.Time `json:"lastReportedAt"`
}

func (s *Store) OfflineLicenseCode(ctx context.Context, actor, id string) (string, error) {
	var code string
	err := RunInTx(ctx, s.DB, func(ctx context.Context, tx bun.Tx) error {
		license := new(model.License)
		if err := tx.NewSelect().Model(license).Where("id=? AND fulfillment_mode='offline_file'", id).Scan(ctx); err != nil {
			return err
		}
		code = s.DecryptLicenseKey(license)
		if code == "" {
			return errors.New("license code unavailable")
		}
		return LocalAuditIn(ctx, tx, actor, "offline-license", id, "view-activation-code", nil)
	})
	if errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	return code, err
}

type OfflineLicenseFilter struct {
	Expiring                                     bool
	Search, CustomerID, TermState, DeliveryState string
	Page, Size                                   int
}

const offlineTermSQL = "CASE WHEN l.current_revision=0 THEN 'unactivated' WHEN l.valid_from>now() THEN 'not_yet_valid' WHEN l.valid_until IS NULL THEN 'perpetual' WHEN l.valid_until<=now() THEN 'expired' ELSE 'valid' END"
const offlineDeliverySQL = "CASE WHEN EXISTS(SELECT 1 FROM license_deliveries d WHERE d.license_id=l.id AND (d.issuance_id IS NULL OR d.issuance_id=i.id)) THEN 'recorded' ELSE 'pending' END"

func (s *Store) offlineLicenseQuery() *bun.SelectQuery {
	return s.DB.NewSelect().TableExpr("licenses l").ColumnExpr("l.id,'C-'||upper(replace(c.id,'-','')) AS customer_number,'L-'||upper(replace(l.id,'-','')) AS license_number,l.display_number,c.id AS customer_id,c.display_name AS customer_name,c.contact_method,c.source_text,l.device_limit,l.term_kind,l.term_amount,l.term_end_date,l.term_starts_at,CASE WHEN l.current_revision=0 THEN NULL ELSE l.valid_from END AS not_before,l.valid_until AS expires_at,l.current_revision AS revision,COALESCE(i.id,'') AS issuance_id,COALESCE(i.key_id,'') AS key_id,COALESCE(i.file_name,'') AS file_name,COALESCE(i.sha256,'') AS sha256,l.issuance_enabled,c.issuance_enabled AS customer_issuance_enabled, 'unreported' AS usage_state,COALESCE(d.method,'') AS delivery_method,COALESCE(d.recipient,'') AS delivery_recipient,d.delivered_at," + offlineTermSQL + " AS term_state," + offlineDeliverySQL + " AS delivery_state").Join("JOIN commercial_customers c ON c.id=l.customer_id").Join("LEFT JOIN license_issuances i ON i.id=l.current_issuance_id").Join("LEFT JOIN LATERAL (SELECT method,recipient,delivered_at FROM license_deliveries WHERE license_id=l.id AND (issuance_id IS NULL OR issuance_id=i.id) ORDER BY delivered_at DESC,created_at DESC,id DESC LIMIT 1) d ON true").Where("l.fulfillment_mode='offline_file'")
}

func (s *Store) offlineDeviceViews(ctx context.Context, licenseIDs []string) (map[string][]OfflineDeviceView, error) {
	rows := []struct {
		LicenseID    string
		BindingID    string
		DeviceLabel  string
		SupersededAt *time.Time
		AppVersion   string
		BuildID      string
		LastSeenAt   *time.Time
	}{}
	if len(licenseIDs) == 0 {
		return map[string][]OfflineDeviceView{}, nil
	}
	err := s.DB.NewRaw(`SELECT license_id,id AS binding_id,device_label,superseded_at,app_version,build_id,last_seen_at
		FROM offline_device_bindings WHERE license_id IN (?) ORDER BY license_id,registered_at,id`, bun.In(licenseIDs)).Scan(ctx, &rows)
	if err != nil {
		return nil, err
	}
	devices := make(map[string][]OfflineDeviceView, len(licenseIDs))
	for _, row := range rows {
		state := "current"
		if row.SupersededAt != nil {
			state = "transferred"
		}
		devices[row.LicenseID] = append(devices[row.LicenseID], OfflineDeviceView{row.BindingID, row.DeviceLabel, state, row.AppVersion, row.BuildID, row.LastSeenAt})
	}
	return devices, nil
}
func (s *Store) OfflineLicenses(ctx context.Context, f OfflineLicenseFilter) ([]OfflineLicenseView, int, error) {
	rows := []OfflineLicenseView{}
	q := s.offlineLicenseQuery()
	if f.Expiring {
		q.Where("l.current_revision>0 AND l.valid_from<=now() AND l.valid_until>now() AND l.valid_until<=now()+interval '15 days'")
	}
	if f.Search != "" {
		term := "%" + f.Search + "%"
		q.Where("(c.display_name ILIKE ? OR l.display_number ILIKE ? OR c.contact_method ILIKE ? OR c.source_text ILIKE ? OR ('C-'||upper(replace(c.id,'-',''))) ILIKE ? OR ('L-'||upper(replace(l.id,'-',''))) ILIKE ? OR EXISTS(SELECT 1 FROM offline_device_bindings sb WHERE sb.license_id=l.id AND sb.device_label ILIKE ?))", term, term, term, term, term, term, term)
	}
	if f.CustomerID != "" {
		q.Where("c.id=?", f.CustomerID)
	}
	if f.TermState == "active" {
		q.Where("(" + offlineTermSQL + ") IN ('valid','perpetual')")
	} else if f.TermState != "" {
		q.Where("("+offlineTermSQL+")=?", f.TermState)
	}
	if f.DeliveryState != "" {
		q.Where("("+offlineDeliverySQL+")=?", f.DeliveryState)
	}
	count, err := q.OrderExpr("l.updated_at DESC,l.id").Limit(f.Size).Offset((f.Page-1)*f.Size).ScanAndCount(ctx, &rows)
	if err != nil {
		return nil, 0, err
	}
	ids := make([]string, len(rows))
	for i := range rows {
		ids[i] = rows[i].ID
	}
	devices, err := s.offlineDeviceViews(ctx, ids)
	for i := range rows {
		rows[i].Devices = devices[rows[i].ID]
		if rows[i].Devices == nil {
			rows[i].Devices = []OfflineDeviceView{}
		}
		rows[i].setTerm()
	}
	return rows, count, err
}
func (s *Store) OfflineLicense(ctx context.Context, id string) (*OfflineLicenseView, error) {
	row := new(OfflineLicenseView)
	err := s.offlineLicenseQuery().Where("l.id=?", id).Scan(ctx, row)
	if err == nil {
		devices, e := s.offlineDeviceViews(ctx, []string{id})
		err = e
		row.Devices = devices[id]
		if row.Devices == nil {
			row.Devices = []OfflineDeviceView{}
		}
		row.setTerm()
	}
	return row, err
}

func (row *OfflineLicenseView) setTerm() {
	var endDate *string
	if row.TermEndDate != nil {
		value := row.TermEndDate.Format("2006-01-02")
		endDate = &value
	}
	row.Term = OfflineTermView{Kind: row.TermKind, Amount: row.TermAmount, EndDate: endDate, StartsAt: row.TermStartsAt}
	row.ActivationAnchor = "first-successful-activation"
	if row.TermStartsAt != nil {
		row.ActivationAnchor = "scheduled"
	}
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
	err = s.DB.NewSelect().Model(&deliveries).Where("license_id=?", id).OrderExpr("created_at DESC,id").Scan(ctx)
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
 count(*) FILTER(WHERE l.current_revision>0 AND l.valid_from<=now() AND (l.valid_until IS NULL OR l.valid_until>now())) AS valid,
 count(*) FILTER(WHERE l.valid_until<=now()) AS expired,
 count(*) FILTER(WHERE l.current_revision>0 AND l.valid_from<=now() AND l.valid_until>now() AND l.valid_until<=now()+interval '15 days') AS expiring,
 count(*) FILTER(WHERE NOT EXISTS(SELECT 1 FROM license_deliveries d WHERE d.license_id=l.id AND (d.issuance_id IS NULL OR d.issuance_id=i.id))) AS pending_delivery,
 count(*) FILTER(WHERE NOT l.issuance_enabled OR NOT c.issuance_enabled) AS stopped,'unreported' AS usage_state
 FROM licenses l JOIN commercial_customers c ON c.id=l.customer_id LEFT JOIN license_issuances i ON i.id=l.current_issuance_id WHERE l.fulfillment_mode='offline_file'`).Scan(ctx, &row)
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
		if err := EnsureOfflineActor(ctx, tx, actor); err != nil {
			return err
		}
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
			if err := AllowOfflineWriteIn(ctx, tx, "state"); err != nil {
				return err
			}
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
