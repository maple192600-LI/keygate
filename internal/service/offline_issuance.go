package service

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/tabloy/keygate/internal/license"
	"github.com/tabloy/keygate/internal/model"
	"github.com/tabloy/keygate/internal/offline"
	"github.com/tabloy/keygate/internal/store"
	"github.com/uptrace/bun"
	"strings"
	"time"
)

type OfflineIssuanceService struct {
	Store         *store.Store
	Keys          *offline.KeyStore
	PreviewSecret []byte
	fault         func(string) error
	clock         func() time.Time
}

func (s *OfflineIssuanceService) now() time.Time {
	if s.clock != nil {
		return s.clock()
	}
	return time.Now()
}

type OfflineIntent struct {
	Action           string       `json:"action"`
	LicenseID        string       `json:"licenseId,omitempty"`
	CustomerID       string       `json:"customerId,omitempty"`
	RequestID        string       `json:"requestId,omitempty"`
	DeviceLabel      string       `json:"deviceLabel,omitempty"`
	ExpectedRevision int64        `json:"expectedRevision,omitempty"`
	Term             offline.Term `json:"term"`
	Reason           string       `json:"reason,omitempty"`
}
type OfflineIssueCommand struct {
	OfflineIntent
	OperationID string `json:"operationId"`
	PreviewBase string `json:"previewBase"`
}
type OfflinePreview struct {
	offline.TermResult
	BaseTime     string `json:"baseTime"`
	PreviewBase  string `json:"previewBase"`
	CustomerName string `json:"customerName"`
	DeviceLabel  string `json:"deviceLabel"`
	DeviceCode   string `json:"deviceCode"`
}
type OfflineIssueResult struct {
	LicenseID  string  `json:"licenseId"`
	IssuanceID string  `json:"issuanceId"`
	Revision   int64   `json:"revision"`
	SHA256     string  `json:"sha256"`
	FileName   string  `json:"fileName"`
	KeyID      string  `json:"keyId"`
	NotBefore  string  `json:"notBefore"`
	ExpiresAt  *string `json:"expiresAt"`
	Replayed   bool    `json:"replayed"`
}
type previewProof struct {
	BaseTime  string `json:"baseTime"`
	InputHash string `json:"inputHash"`
}

func offlineFailure(code, message string) error { return &offline.Error{Code: code, Message: message} }
func hashJSON(value any) string {
	raw, _ := json.Marshal(value)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func cleanIntent(input OfflineIntent) (OfflineIntent, error) {
	input.Reason = strings.TrimSpace(input.Reason)
	for _, id := range []string{input.CustomerID, input.RequestID, input.LicenseID} {
		if id != "" && !offline.ValidUUID4(id) {
			return input, offlineFailure("BAD_REQUEST", "业务编号无效，请重新选择客户、申请或授权")
		}
	}
	if len([]rune(input.Reason)) > 1000 || len([]rune(input.DeviceLabel)) > 80 {
		return input, offlineFailure("BAD_REQUEST", "换机原因或设备名称过长")
	}
	if input.Action != "initial" && input.Action != "renew" && input.Action != "transfer" {
		return input, offlineFailure("BAD_REQUEST", "不支持的签发动作")
	}
	if input.Action == "initial" {
		if input.LicenseID != "" || input.ExpectedRevision != 0 || input.CustomerID == "" || input.RequestID == "" {
			return input, offlineFailure("BAD_REQUEST", "首次签发缺少客户或设备申请")
		}
	}
	if input.Action != "initial" && (input.LicenseID == "" || input.ExpectedRevision < 1) {
		return input, offlineFailure("BAD_REQUEST", "请重新打开授权详情后操作")
	}
	if input.Action == "transfer" && (input.RequestID == "" || input.Reason == "") {
		return input, offlineFailure("BAD_REQUEST", "换机须提供新机申请及原因")
	}
	if input.Action == "transfer" && (input.Term.Kind != "" || input.Term.Amount != 0 || input.Term.EndDate != "" || input.Term.StartsAt != "") {
		return input, offlineFailure("BAD_REQUEST", "换机保留原期限，请勿同时设置新期限")
	}
	return input, nil
}
func (s *OfflineIssuanceService) proof(input OfflineIntent, now time.Time) (previewProof, string) {
	p := previewProof{now.UTC().Truncate(time.Second).Format("2006-01-02T15:04:05.000Z"), hashJSON(input)}
	raw, _ := json.Marshal(p)
	h := hmac.New(sha256.New, s.PreviewSecret)
	h.Write([]byte("snail-license-preview/v1\x00"))
	h.Write(raw)
	return p, base64.RawURLEncoding.EncodeToString(raw) + "." + base64.RawURLEncoding.EncodeToString(h.Sum(nil))
}
func (s *OfflineIssuanceService) verifyProof(input OfflineIntent, token string) (time.Time, error) {
	if len(token) > 4096 {
		return time.Time{}, offlineFailure("PREVIEW_EXPIRED", "签发预览已失效，请重新预览")
	}
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return time.Time{}, offlineFailure("PREVIEW_EXPIRED", "请先预览并核对签发条件")
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(parts[0])
	if err != nil {
		return time.Time{}, offlineFailure("PREVIEW_EXPIRED", "预览内容无效，请重新预览")
	}
	signature, err := base64.RawURLEncoding.Strict().DecodeString(parts[1])
	if err != nil {
		return time.Time{}, err
	}
	h := hmac.New(sha256.New, s.PreviewSecret)
	h.Write([]byte("snail-license-preview/v1\x00"))
	h.Write(raw)
	if !hmac.Equal(signature, h.Sum(nil)) {
		return time.Time{}, offlineFailure("PREVIEW_EXPIRED", "预览条件已改变，请重新预览")
	}
	var proof previewProof
	if err = json.Unmarshal(raw, &proof); err != nil || proof.InputHash != hashJSON(input) {
		return time.Time{}, offlineFailure("PREVIEW_EXPIRED", "预览条件已改变，请重新预览")
	}
	base, err := offline.ParseUTC(proof.BaseTime)
	if err != nil {
		return time.Time{}, err
	}
	now := s.now()
	if now.Before(base) || now.Sub(base) > 10*time.Minute {
		return time.Time{}, offlineFailure("PREVIEW_EXPIRED", "预览超过 10 分钟，请重新核对")
	}
	return base, nil
}

type issuanceContext struct {
	customer *model.CommercialCustomer
	license  *model.License
	request  *model.OfflineRequest
	previous *model.LicenseIssuance
	binding  *model.OfflineDeviceBinding
}

func (s *OfflineIssuanceService) load(ctx context.Context, db bun.IDB, input OfflineIntent, lock bool) (issuanceContext, error) {
	out := issuanceContext{}
	customerID := input.CustomerID
	requestID := input.RequestID
	if input.Action != "initial" {
		out.license = new(model.License)
		q := db.NewSelect().Model(out.license).Where("id = ? AND fulfillment_mode='offline_file'", input.LicenseID)
		if lock {
			q.For("UPDATE")
		}
		if err := q.Scan(ctx); err != nil {
			return out, err
		}
		if out.license.CurrentRevision != input.ExpectedRevision {
			return out, offlineFailure("REVISION_CONFLICT", "授权已有新版本，请刷新并核对最新条件")
		}
		if !out.license.IssuanceEnabled {
			return out, offlineFailure("ISSUANCE_STOPPED", "此授权已停止续发")
		}
		if customerID != "" && customerID != out.license.CustomerID {
			return out, offlineFailure("BAD_REQUEST", "客户与当前授权不一致")
		}
		customerID = out.license.CustomerID
		out.previous = new(model.LicenseIssuance)
		if err := db.NewSelect().Model(out.previous).Where("id = ? AND license_id = ?", out.license.CurrentIssuanceID, out.license.ID).Scan(ctx); err != nil {
			return out, err
		}
		out.binding = new(model.OfflineDeviceBinding)
		if err := db.NewSelect().Model(out.binding).Where("id = ? AND license_id = ?", out.previous.BindingID, out.license.ID).Scan(ctx); err != nil {
			return out, err
		}
		if input.Action == "renew" {
			if requestID != "" && requestID != out.previous.RequestID {
				return out, offlineFailure("BAD_REQUEST", "续期不能同时换机")
			}
			requestID = out.previous.RequestID
			if input.DeviceLabel != "" && input.DeviceLabel != out.binding.DeviceLabel {
				return out, offlineFailure("BAD_REQUEST", "续期不能改写设备登记")
			}
		}
	}
	out.customer = new(model.CommercialCustomer)
	q := db.NewSelect().Model(out.customer).Where("id = ?", customerID)
	if lock {
		q.For("SHARE")
	}
	if err := q.Scan(ctx); err != nil {
		return out, err
	}
	if !out.customer.IssuanceEnabled {
		return out, offlineFailure("ISSUANCE_STOPPED", "此客户已停止续发")
	}
	if out.customer.ContactMethod == "" && (out.customer.Email == nil || *out.customer.Email == "") {
		return out, offlineFailure("CUSTOMER_CONTACT_REQUIRED", "发放前请补充客户联系方式或邮箱")
	}
	out.request = new(model.OfflineRequest)
	if err := db.NewSelect().Model(out.request).Where("request_id = ?", requestID).Scan(ctx); err != nil {
		return out, err
	}
	if out.request.ProductCode != offline.ProductID {
		return out, offlineFailure("LICENSE_WRONG_PRODUCT", "申请不属于蜗牛财务")
	}
	if input.Action == "transfer" && out.binding.DeviceType == out.request.DeviceType && out.binding.DeviceDigest == out.request.DeviceDigest {
		return out, offlineFailure("BAD_REQUEST", "新机申请与当前设备相同，请核对设备")
	}
	return out, nil
}
func periodFor(data issuanceContext) *offline.Period {
	if data.previous == nil {
		return nil
	}
	p := &offline.Period{NotBefore: data.previous.NotBefore.UTC().Format("2006-01-02T15:04:05.000Z")}
	if data.previous.ExpiresAt != nil {
		value := data.previous.ExpiresAt.UTC().Format("2006-01-02T15:04:05.000Z")
		p.ExpiresAt = &value
	}
	return p
}
func (s *OfflineIssuanceService) Preview(ctx context.Context, input OfflineIntent) (OfflinePreview, error) {
	input, err := cleanIntent(input)
	if err != nil {
		return OfflinePreview{}, err
	}
	data, err := s.load(ctx, s.Store.DB, input, false)
	if err != nil {
		return OfflinePreview{}, err
	}
	proof, token := s.proof(input, s.now())
	now, _ := offline.ParseUTC(proof.BaseTime)
	term, err := offline.CalculateTerm(now, input.Action, input.Term, periodFor(data))
	if err != nil {
		return OfflinePreview{}, err
	}
	label := data.request.DeviceLabel
	if input.DeviceLabel != "" {
		label = input.DeviceLabel
	}
	if input.Action == "renew" {
		label = data.binding.DeviceLabel
	}
	return OfflinePreview{term, proof.BaseTime, token, data.customer.DisplayName, label, "STQ-WIN1-" + data.request.DeviceDigest}, nil
}
func IssuanceResult(row *model.LicenseIssuance, replayed bool) OfflineIssueResult {
	result := OfflineIssueResult{LicenseID: row.LicenseID, IssuanceID: row.ID, Revision: row.Revision, SHA256: row.SHA256, FileName: row.FileName, KeyID: row.KeyID, NotBefore: row.NotBefore.UTC().Format("2006-01-02T15:04:05.000Z"), Replayed: replayed}
	if row.ExpiresAt != nil {
		value := row.ExpiresAt.UTC().Format("2006-01-02T15:04:05.000Z")
		result.ExpiresAt = &value
	}
	return result
}
func (s *OfflineIssuanceService) Issue(ctx context.Context, actor string, command OfflineIssueCommand) (OfflineIssueResult, error) {
	intent, err := cleanIntent(command.OfflineIntent)
	if err != nil {
		return OfflineIssueResult{}, err
	}
	command.OfflineIntent = intent
	if !offline.ValidUUID4(command.OperationID) {
		return OfflineIssueResult{}, offlineFailure("BAD_REQUEST", "签发操作号须为 UUID v4")
	}
	operationHash := hashJSON(command)
	var result OfflineIssueResult
	err = store.RunInTx(ctx, s.Store.DB, func(ctx context.Context, tx bun.Tx) error {
		if _, err := tx.NewRaw("SELECT pg_advisory_xact_lock(hashtextextended(?,0))", "snail-issuance:"+command.OperationID).Exec(ctx); err != nil {
			return err
		}
		existing := new(model.LicenseIssuance)
		err := tx.NewSelect().Model(existing).Where("operation_id = ?", command.OperationID).Scan(ctx)
		if err == nil {
			if existing.OperationHash != operationHash {
				return offlineFailure("OPERATION_CONFLICT", "该操作号已用于不同条件，请查询原签发结果")
			}
			result = IssuanceResult(existing, true)
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		base, err := s.verifyProof(intent, command.PreviewBase)
		if err != nil {
			return err
		}
		data, err := s.load(ctx, tx, intent, true)
		if err != nil {
			return err
		}
		term, err := offline.CalculateTerm(base, intent.Action, intent.Term, periodFor(data))
		if err != nil {
			return err
		}
		if term.ExpiresAt != nil {
			end, _ := offline.ParseUTC(*term.ExpiresAt)
			if !s.now().Before(end) {
				return offlineFailure("LICENSE_EXPIRED", "授权期限已到，请重新预览有效期限")
			}
		}
		if intent.Action == "initial" {
			var productID, planID string
			if err := tx.NewRaw("SELECT p.id,pl.id FROM products p JOIN plans pl ON pl.product_id=p.id WHERE p.license_product_code=? AND pl.slug='offline-standard'", offline.ProductID).Scan(ctx, &productID, &planID); err != nil {
				return err
			}
			var number int64
			if err := tx.NewRaw("SELECT nextval('snail_license_display_seq')").Scan(ctx, &number); err != nil {
				return err
			}
			start, _ := offline.ParseUTC(term.NotBefore)
			data.license = &model.License{ID: store.NewID(), ProductID: productID, PlanID: planID, CustomerID: data.customer.ID, FulfillmentMode: "offline_file", IssuanceEnabled: true, LicenseKey: license.GenerateKey("KG"), DisplayNumber: fmt.Sprintf("SN-%s-%04d", base.In(time.FixedZone("Asia/Shanghai", 8*3600)).Format("200601"), number), Status: "active", ValidFrom: start}
			if err := s.Store.CreateOfflineLicenseIn(ctx, tx, data.license); err != nil {
				return err
			}
		}
		binding := data.binding
		if intent.Action != "renew" {
			binding = &model.OfflineDeviceBinding{ID: store.NewID(), LicenseID: data.license.ID, DeviceType: data.request.DeviceType, DeviceDigest: data.request.DeviceDigest, DeviceLabel: data.request.DeviceLabel}
			if intent.DeviceLabel != "" {
				binding.DeviceLabel = intent.DeviceLabel
			}
			if intent.Action == "transfer" {
				binding.PreviousBindingID = data.binding.ID
				if _, err := tx.NewUpdate().Model((*model.OfflineDeviceBinding)(nil)).Set("superseded_at=now()").Where("id = ?", data.binding.ID).Exec(ctx); err != nil {
					return err
				}
			}
			if _, err := tx.NewInsert().Model(binding).Exec(ctx); err != nil {
				return err
			}
		}
		issuanceID := store.NewID()
		payload := offline.Payload{FormatVersion: 2, LicenseID: data.license.ID, IssuanceID: issuanceID, ProductID: offline.ProductID, Purpose: "commercial", CustomerID: data.customer.ID, Revision: data.license.CurrentRevision + 1, IssuedAt: base.UTC().Format("2006-01-02T15:04:05.000Z"), NotBefore: term.NotBefore, ExpiresAt: term.ExpiresAt, Binding: offline.Device{Type: binding.DeviceType, Digest: binding.DeviceDigest}}
		raw, payload, err := s.Keys.Sign(payload)
		if err != nil {
			return err
		}
		if s.fault != nil {
			if err = s.fault("signed"); err != nil {
				return err
			}
		}
		sum := sha256.Sum256(raw)
		start, _ := offline.ParseUTC(payload.NotBefore)
		row := &model.LicenseIssuance{ID: issuanceID, LicenseID: data.license.ID, Revision: payload.Revision, OperationID: command.OperationID, OperationHash: operationHash, Kind: intent.Action, RequestID: data.request.RequestID, BindingID: binding.ID, KeyID: payload.KeyID, NotBefore: start, IssuedAt: base, RawBytes: raw, SHA256: hex.EncodeToString(sum[:]), FileName: fmt.Sprintf("蜗牛授权_%s_r%d.snaillic", data.license.DisplayNumber, payload.Revision), CreatedBy: actor}
		if payload.ExpiresAt != nil {
			end, _ := offline.ParseUTC(*payload.ExpiresAt)
			row.ExpiresAt = &end
		}
		if data.previous != nil {
			row.PreviousIssuanceID = data.previous.ID
		}
		if _, err = tx.NewInsert().Model(row).Exec(ctx); err != nil {
			return err
		}
		if s.fault != nil {
			if err = s.fault("archived"); err != nil {
				return err
			}
		}
		if _, err = tx.NewUpdate().Model((*model.License)(nil)).Set("current_revision = ?", row.Revision).Set("current_issuance_id = ?", row.ID).Set("valid_from = ?", row.NotBefore).Set("valid_until = ?", row.ExpiresAt).Set("updated_at=now()").Where("id = ?", row.LicenseID).Exec(ctx); err != nil {
			return err
		}
		if err = store.LocalAuditIn(ctx, tx, actor, "offline-license", row.LicenseID, intent.Action, map[string]any{"issuanceId": row.ID, "revision": row.Revision, "sha256": row.SHA256, "reason": intent.Reason}); err != nil {
			return err
		}
		if s.fault != nil {
			if err = s.fault("audited"); err != nil {
				return err
			}
		}
		result = IssuanceResult(row, false)
		return nil
	})
	return result, err
}
