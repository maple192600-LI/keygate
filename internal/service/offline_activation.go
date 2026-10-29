package service

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/tabloy/keygate/internal/license"
	"github.com/tabloy/keygate/internal/model"
	"github.com/tabloy/keygate/internal/offline"
	"github.com/tabloy/keygate/internal/store"
	"github.com/uptrace/bun"
)

const clientServiceActor = store.ClientActivationServiceActorID

type ClientActivationService struct {
	Store *store.Store
	Keys  *offline.KeyStore
	clock func() time.Time
}

type ClientActivationCommand struct {
	OperationID string         `json:"operationId"`
	LicenseCode string         `json:"licenseCode"`
	ProductID   string         `json:"productId"`
	Device      offline.Device `json:"device"`
	DeviceLabel string         `json:"deviceLabel"`
	AppVersion  string         `json:"appVersion"`
	BuildID     string         `json:"buildId"`
}

type ClientSyncCommand struct {
	RawLicense      string          `json:"rawLicense"`
	Device          offline.Device  `json:"device"`
	DeviceLabel     string          `json:"deviceLabel"`
	AppVersion      string          `json:"appVersion"`
	BuildID         string          `json:"buildId"`
	RecoveryRequest json.RawMessage `json:"recoveryRequest,omitempty"`
}

type ClientLicenseResult struct {
	RawLicense     string `json:"rawLicense"`
	LicenseID      string `json:"licenseId"`
	CustomerNumber string `json:"customerNumber"`
	LicenseNumber  string `json:"licenseNumber"`
	BindingID      string `json:"bindingId"`
	ServerTime     string `json:"serverTime"`
	RawRecovery    string `json:"rawRecovery,omitempty"`
}

func (s *ClientActivationService) now() time.Time {
	if s.clock != nil {
		return s.clock()
	}
	return time.Now()
}

func clientFailure(code, message string) error { return &offline.Error{Code: code, Message: message} }

func cleanClientDevice(device offline.Device, label, appVersion, buildID string) (offline.Device, string, string, string, error) {
	label = strings.TrimSpace(label)
	appVersion = strings.TrimSpace(appVersion)
	buildID = strings.TrimSpace(buildID)
	if device.Type != offline.MachineType || len(device.Digest) != 64 {
		return device, "", "", "", clientFailure("DEVICE_INVALID", "设备身份无效，请更新客户端后重试")
	}
	for _, value := range device.Digest {
		if (value < '0' || value > '9') && (value < 'a' || value > 'f') {
			return device, "", "", "", clientFailure("DEVICE_INVALID", "设备身份无效，请更新客户端后重试")
		}
	}
	if utf8.RuneCountInString(label) > 80 || utf8.RuneCountInString(appVersion) > 40 || utf8.RuneCountInString(buildID) > 100 {
		return device, "", "", "", clientFailure("BAD_REQUEST", "设备或版本信息过长")
	}
	return device, label, appVersion, buildID, nil
}

func cleanClientActivation(input ClientActivationCommand) (ClientActivationCommand, error) {
	if !offline.ValidUUID4(input.OperationID) {
		return input, clientFailure("BAD_REQUEST", "激活操作编号无效，请重试")
	}
	input.LicenseCode = strings.ToUpper(strings.TrimSpace(input.LicenseCode))
	if len(input.LicenseCode) < 16 || len(input.LicenseCode) > 64 || input.ProductID != offline.ProductID {
		return input, clientFailure("LICENSE_INVALID", "授权码或产品无效，请核对后重试")
	}
	device, label, version, build, err := cleanClientDevice(input.Device, input.DeviceLabel, input.AppVersion, input.BuildID)
	if err != nil {
		return input, err
	}
	input.Device, input.DeviceLabel, input.AppVersion, input.BuildID = device, label, version, build
	return input, nil
}

func cleanClientSync(input ClientSyncCommand) (ClientSyncCommand, error) {
	if len(input.RawLicense) == 0 || len(input.RawLicense) > offline.LicenseLimit || !utf8.ValidString(input.RawLicense) {
		return input, clientFailure("LICENSE_INVALID", "本机授权内容无效，请保留本机资料并重试")
	}
	if len(input.RecoveryRequest) > offline.RequestLimit {
		return input, clientFailure("RECOVERY_REQUEST_INVALID", "恢复请求无效，请重新生成后重试")
	}
	device, label, version, build, err := cleanClientDevice(input.Device, input.DeviceLabel, input.AppVersion, input.BuildID)
	if err != nil {
		return input, err
	}
	input.Device, input.DeviceLabel, input.AppVersion, input.BuildID = device, label, version, build
	return input, nil
}

func (s *ClientActivationService) trustedPublic(ctx context.Context) (map[string]ed25519.PublicKey, error) {
	physical, err := s.Keys.Status()
	if err != nil {
		return nil, err
	}
	expected, err := s.Store.ExpectedSigningIdentity(ctx, physical)
	if err != nil {
		return nil, err
	}
	if expected.KeyID == "" || expected.PublicKeyPEM == "" {
		return nil, clientFailure("SIGNING_IDENTITY_INVALID", "服务签发身份尚未初始化")
	}
	public, err := offline.ParsePublic([]byte(expected.PublicKeyPEM))
	if err != nil || offline.KeyID(public) != expected.KeyID {
		return nil, clientFailure("SIGNING_IDENTITY_INVALID", "服务签发身份与已登记公钥不一致")
	}
	return map[string]ed25519.PublicKey{expected.KeyID: public}, nil
}

func periodForLicense(row *model.License) offline.Period {
	period := offline.Period{NotBefore: row.ValidFrom.UTC().Format("2006-01-02T15:04:05.000Z")}
	if row.ValidUntil != nil {
		value := row.ValidUntil.UTC().Format("2006-01-02T15:04:05.000Z")
		period.ExpiresAt = &value
	}
	return period
}

func termForLicense(row *model.License) offline.Term {
	term := offline.Term{Kind: row.TermKind, Amount: row.TermAmount}
	if row.TermEndDate != nil {
		term.EndDate = row.TermEndDate.Format("2006-01-02")
	}
	return term
}

type clientBindingToSign struct {
	ID                 string `bun:"id"`
	DeviceType         string `bun:"device_type"`
	DeviceDigest       string `bun:"device_digest"`
	DeviceLabel        string `bun:"device_label"`
	AppVersion         string `bun:"app_version"`
	BuildID            string `bun:"build_id"`
	RequestID          string `bun:"request_id"`
	PreviousIssuanceID string `bun:"previous_issuance_id"`
}

func currentBindings(ctx context.Context, tx bun.IDB, licenseID string) ([]clientBindingToSign, error) {
	rows := []clientBindingToSign{}
	err := tx.NewRaw(`SELECT b.id,b.device_type,b.device_digest,b.device_label,b.app_version,b.build_id,
COALESCE(last.request_id,'') AS request_id,COALESCE(last.id,'') AS previous_issuance_id
FROM offline_device_bindings b
LEFT JOIN LATERAL (SELECT id,request_id FROM license_issuances WHERE binding_id=b.id ORDER BY revision DESC LIMIT 1) last ON true
WHERE b.license_id=? AND b.superseded_at IS NULL ORDER BY b.registered_at,b.id`, licenseID).Scan(ctx, &rows)
	return rows, err
}

func recordClientOperation(ctx context.Context, tx bun.IDB, operationID, operationHash, kind, actor string, value any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = tx.NewRaw("INSERT INTO offline_management_operations(operation_id,operation_hash,kind,result,created_by) VALUES(?,?,?,?,?)", operationID, operationHash, kind, string(encoded), actor).Exec(ctx)
	return err
}

func previousOperation(ctx context.Context, tx bun.IDB, operationID, operationHash string, out any) (bool, error) {
	var row struct {
		OperationHash string
		Result        json.RawMessage
	}
	err := tx.NewRaw("SELECT operation_hash,result FROM offline_management_operations WHERE operation_id=?", operationID).Scan(ctx, &row)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if row.OperationHash != operationHash {
		return false, clientFailure("OPERATION_CONFLICT", "该激活操作编号已用于不同内容")
	}
	if err = json.Unmarshal(row.Result, out); err != nil {
		return false, err
	}
	return true, nil
}

func appendIssuance(ctx context.Context, tx bun.IDB, keys *offline.KeyStore, actor string, licenseRow *model.License, customerID string, binding clientBindingToSign, revision int64, kind, bindingState, operationHash string, period offline.Period, now time.Time) (*model.LicenseIssuance, []byte, error) {
	if binding.RequestID == "" {
		return nil, nil, errors.New("active device has no original activation request")
	}
	issuanceID := store.NewID()
	issuedAt := now.UTC().Truncate(time.Millisecond)
	payload := offline.Payload{
		FormatVersion: 2,
		LicenseID:     licenseRow.ID,
		IssuanceID:    issuanceID,
		ProductID:     offline.ProductID,
		Purpose:       "commercial",
		CustomerID:    customerID,
		Revision:      revision,
		IssuedAt:      issuedAt.Format("2006-01-02T15:04:05.000Z"),
		NotBefore:     period.NotBefore,
		ExpiresAt:     period.ExpiresAt,
		Binding:       offline.Device{Type: binding.DeviceType, Digest: binding.DeviceDigest},
		BindingState:  bindingState,
	}
	raw, signed, err := keys.Sign(payload)
	if err != nil {
		return nil, nil, err
	}
	sum := sha256.Sum256(raw)
	start, err := offline.ParseUTC(signed.NotBefore)
	if err != nil {
		return nil, nil, err
	}
	row := &model.LicenseIssuance{
		ID: issuanceID, LicenseID: licenseRow.ID, Revision: revision, OperationID: store.NewID(),
		OperationHash: operationHash, Kind: kind, RequestID: binding.RequestID, BindingID: binding.ID,
		KeyID: signed.KeyID, NotBefore: start, IssuedAt: issuedAt, RawBytes: raw,
		SHA256: hex.EncodeToString(sum[:]), FileName: fmt.Sprintf("蜗牛授权_%s_r%d.snaillic", licenseRow.DisplayNumber, revision),
		PreviousIssuanceID: binding.PreviousIssuanceID, CreatedBy: actor,
	}
	if signed.ExpiresAt != nil {
		end, parseErr := offline.ParseUTC(*signed.ExpiresAt)
		if parseErr != nil {
			return nil, nil, parseErr
		}
		row.ExpiresAt = &end
	}
	if _, err = tx.NewInsert().Model(row).Exec(ctx); err != nil {
		return nil, nil, err
	}
	return row, raw, nil
}

func (s *ClientActivationService) Activate(ctx context.Context, input ClientActivationCommand) (ClientLicenseResult, error) {
	input, err := cleanClientActivation(input)
	if err != nil {
		return ClientLicenseResult{}, err
	}
	operationHash := hashJSON(input)
	now := s.now().UTC().Truncate(time.Millisecond)
	var result ClientLicenseResult
	err = store.RunInTx(ctx, s.Store.DB, func(ctx context.Context, tx bun.Tx) error {
		if _, err := tx.NewRaw("SELECT pg_advisory_xact_lock(hashtextextended(?,0))", "snail-client-operation:"+input.OperationID).Exec(ctx); err != nil {
			return err
		}
		replayed, err := previousOperation(ctx, tx, input.OperationID, operationHash, &result)
		if err != nil || replayed {
			return err
		}
		licenseRow := new(model.License)
		if err = tx.NewSelect().Model(licenseRow).Where("license.fulfillment_mode='offline_file' AND license.key_hash=?", license.HashKey(input.LicenseCode)).For("UPDATE").Scan(ctx); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return clientFailure("LICENSE_INVALID", "授权码无效，请核对后重试")
			}
			return err
		}
		if licenseRow.FulfillmentMode != "offline_file" || !licenseRow.IssuanceEnabled || licenseRow.DeviceLimit < 1 {
			return clientFailure("LICENSE_UNAVAILABLE", "此授权暂不可激活，请联系服务方")
		}
		var productCode string
		if err = tx.NewRaw("SELECT license_product_code FROM products WHERE id=?", licenseRow.ProductID).Scan(ctx, &productCode); err != nil {
			return err
		}
		if productCode != offline.ProductID {
			return clientFailure("LICENSE_WRONG_PRODUCT", "授权不属于此产品")
		}
		customer := new(model.CommercialCustomer)
		if err = tx.NewSelect().Model(customer).Where("id=?", licenseRow.CustomerID).For("SHARE").Scan(ctx); err != nil {
			return err
		}
		if !customer.IssuanceEnabled {
			return clientFailure("LICENSE_UNAVAILABLE", "此授权暂不可激活，请联系服务方")
		}
		bindings, err := currentBindings(ctx, tx, licenseRow.ID)
		if err != nil {
			return err
		}
		if _, err = tx.NewRaw("INSERT INTO offline_actors(id,display_name) VALUES(?,?) ON CONFLICT(id) DO NOTHING", clientServiceActor, "客户端激活服务").Exec(ctx); err != nil {
			return err
		}
		bindingIndex := -1
		for i := range bindings {
			if bindings[i].DeviceType == input.Device.Type && bindings[i].DeviceDigest == input.Device.Digest {
				bindingIndex = i
				break
			}
		}
		newBinding := bindingIndex < 0
		if newBinding && len(bindings) >= licenseRow.DeviceLimit {
			return clientFailure("LICENSE_DEVICE_LIMIT", "此授权的设备名额已用完，请联系服务方换机")
		}
		if newBinding && licenseRow.CurrentRevision > 0 && licenseRow.ValidUntil != nil && !now.Before(*licenseRow.ValidUntil) {
			return clientFailure("LICENSE_EXPIRED", "授权已到期，请先续期")
		}
		if newBinding {
			request := offline.Request{FormatVersion: 2, RequestID: store.NewID(), ProductID: offline.ProductID, Device: input.Device, DeviceLabel: input.DeviceLabel, AppVersion: input.AppVersion}
			rawRequest, marshalErr := json.Marshal(request)
			if marshalErr != nil {
				return marshalErr
			}
			_, fingerprint, parseErr := offline.ParseRequest(rawRequest, offline.ProductID)
			if parseErr != nil {
				return parseErr
			}
			if _, err = tx.NewInsert().Model(&model.OfflineRequest{RequestID: request.RequestID, ProductCode: request.ProductID, DeviceType: request.Device.Type, DeviceDigest: request.Device.Digest, DeviceLabel: request.DeviceLabel, AppVersion: input.AppVersion, BuildID: input.BuildID, RawBytes: rawRequest, Fingerprint: fingerprint, CreatedBy: clientServiceActor}).Exec(ctx); err != nil {
				return err
			}
			binding := &model.OfflineDeviceBinding{ID: store.NewID(), LicenseID: licenseRow.ID, DeviceType: input.Device.Type, DeviceDigest: input.Device.Digest, DeviceLabel: input.DeviceLabel, AppVersion: input.AppVersion, BuildID: input.BuildID, LastSeenAt: &now}
			if _, err = tx.NewInsert().Model(binding).Exec(ctx); err != nil {
				return err
			}
			bindings = append(bindings, clientBindingToSign{ID: binding.ID, DeviceType: binding.DeviceType, DeviceDigest: binding.DeviceDigest, DeviceLabel: binding.DeviceLabel, AppVersion: binding.AppVersion, BuildID: binding.BuildID, RequestID: request.RequestID})
			bindingIndex = len(bindings) - 1
		} else {
			binding := &bindings[bindingIndex]
			if input.DeviceLabel != "" {
				binding.DeviceLabel = input.DeviceLabel
			}
			binding.AppVersion, binding.BuildID = input.AppVersion, input.BuildID
			if _, err = tx.NewRaw("UPDATE offline_device_bindings SET device_label=?,app_version=?,build_id=?,last_seen_at=? WHERE id=? AND superseded_at IS NULL", binding.DeviceLabel, binding.AppVersion, binding.BuildID, now, binding.ID).Exec(ctx); err != nil {
				return err
			}
		}
		period := periodForLicense(licenseRow)
		if licenseRow.CurrentRevision == 0 {
			start := now
			if licenseRow.TermStartsAt != nil {
				start = *licenseRow.TermStartsAt
			}
			calculated, calcErr := offline.CalculateTerm(start, "initial", termForLicense(licenseRow), nil)
			if calcErr != nil {
				return calcErr
			}
			period = calculated.Period
			if period.ExpiresAt != nil {
				end, _ := offline.ParseUTC(*period.ExpiresAt)
				if !now.Before(end) {
					return clientFailure("LICENSE_EXPIRED", "授权期限已结束，请联系服务方")
				}
			}
		} else if licenseRow.ValidUntil != nil && !now.Before(*licenseRow.ValidUntil) {
			return clientFailure("LICENSE_EXPIRED", "授权已到期，请先续期")
		}
		if licenseRow.CurrentRevision >= 9007199254740991 {
			return clientFailure("LICENSE_REVISION_LIMIT", "授权版本超出支持范围，请联系服务方")
		}
		revision := licenseRow.CurrentRevision + 1
		if !newBinding {
			revision = licenseRow.CurrentRevision
			if revision == 0 {
				return errors.New("existing device binding has no signed issuance")
			}
		}
		if newBinding {
			if _, err = s.trustedPublic(ctx); err != nil {
				return err
			}
		}
		binding := bindings[bindingIndex]
		var rawLicense []byte
		var currentIssuanceID string
		if newBinding {
			bindingsToSign := bindings
			for i := range bindingsToSign {
				if i != bindingIndex && bindingsToSign[i].PreviousIssuanceID == "" {
					return errors.New("existing active device has no signed issuance")
				}
				issuanceKind := "activate"
				if licenseRow.CurrentRevision == 0 {
					issuanceKind = "initial"
				}
				operationInput := struct {
					OperationID, BindingID string
					Revision               int64
				}{input.OperationID, bindingsToSign[i].ID, revision}
				rowHash := hashJSON(operationInput)
				row, signedBytes, issueErr := appendIssuance(ctx, tx, s.Keys, clientServiceActor, licenseRow, customer.ID, bindingsToSign[i], revision, issuanceKind, "active", rowHash, period, now)
				if issueErr != nil {
					return issueErr
				}
				if bindingsToSign[i].ID == binding.ID {
					rawLicense = signedBytes
				}
				if i == bindingIndex {
					currentIssuanceID = row.ID
				}
				bindingsToSign[i].PreviousIssuanceID = row.ID
			}
			if err = store.AllowOfflineWriteIn(ctx, tx, "issue"); err != nil {
				return err
			}
			start, _ := offline.ParseUTC(period.NotBefore)
			var end *time.Time
			if period.ExpiresAt != nil {
				parsed, parseErr := offline.ParseUTC(*period.ExpiresAt)
				if parseErr != nil {
					return parseErr
				}
				end = &parsed
			}
			if _, err = tx.NewRaw("UPDATE licenses SET current_revision=?,current_issuance_id=?,valid_from=?,valid_until=?,status='active',updated_at=now() WHERE id=?", revision, currentIssuanceID, start, end, licenseRow.ID).Exec(ctx); err != nil {
				return err
			}
			licenseRow.CurrentRevision, licenseRow.CurrentIssuanceID = revision, currentIssuanceID
			licenseRow.ValidFrom, licenseRow.ValidUntil = start, end
		} else {
			var latest model.LicenseIssuance
			if err = tx.NewSelect().Model(&latest).Where("license_id=? AND binding_id=?", licenseRow.ID, binding.ID).OrderExpr("revision DESC").Limit(1).Scan(ctx); err != nil {
				return err
			}
			if latest.Revision != licenseRow.CurrentRevision {
				return clientFailure("LICENSE_REVISION_CONFLICT", "授权记录需要同步，请稍后重试")
			}
			sum := sha256.Sum256(latest.RawBytes)
			if hex.EncodeToString(sum[:]) != latest.SHA256 {
				return clientFailure("LICENSE_ARCHIVE_DAMAGED", "授权记录校验失败，请联系服务方")
			}
			rawLicense = latest.RawBytes
			currentIssuanceID = latest.ID
		}
		if len(rawLicense) == 0 {
			return errors.New("activation produced no signed license")
		}
		if licenseRow.CurrentRevision == 0 || newBinding {
			result = clientLicenseResult(rawLicense, licenseRow.ID, customer.ID, binding.ID, now)
			if err = store.LocalAuditIn(ctx, tx, clientServiceActor, "offline-license", licenseRow.ID, "client-activate", map[string]any{"bindingId": binding.ID, "revision": revision, "newDevice": newBinding}); err != nil {
				return err
			}
			if err = recordClientOperation(ctx, tx, input.OperationID, operationHash, "client-activate", clientServiceActor, result); err != nil {
				return err
			}
		} else {
			result = clientLicenseResult(rawLicense, licenseRow.ID, customer.ID, binding.ID, now)
			if err = store.LocalAuditIn(ctx, tx, clientServiceActor, "offline-license", licenseRow.ID, "client-reactivate", map[string]any{"bindingId": binding.ID, "revision": licenseRow.CurrentRevision}); err != nil {
				return err
			}
			if err = recordClientOperation(ctx, tx, input.OperationID, operationHash, "client-activate", clientServiceActor, result); err != nil {
				return err
			}
		}
		return nil
	})
	return result, err
}

func clientLicenseResult(raw []byte, licenseID, customerID, bindingID string, now time.Time) ClientLicenseResult {
	return ClientLicenseResult{RawLicense: string(raw), LicenseID: licenseID, CustomerNumber: "C-" + strings.ToUpper(strings.ReplaceAll(customerID, "-", "")), LicenseNumber: "L-" + strings.ToUpper(strings.ReplaceAll(licenseID, "-", "")), BindingID: bindingID, ServerTime: now.UTC().Truncate(time.Millisecond).Format("2006-01-02T15:04:05.000Z")}
}

func (s *ClientActivationService) Sync(ctx context.Context, input ClientSyncCommand) (ClientLicenseResult, error) {
	input, err := cleanClientSync(input)
	if err != nil {
		return ClientLicenseResult{}, err
	}
	raw := []byte(input.RawLicense)
	public, err := s.trustedPublic(ctx)
	if err != nil {
		return ClientLicenseResult{}, err
	}
	payload, err := offline.Verify(raw, public, offline.ProductID)
	if err != nil {
		return ClientLicenseResult{}, err
	}
	if payload.Binding != input.Device {
		return ClientLicenseResult{}, clientFailure("LICENSE_DEVICE_MISMATCH", "本机与授权绑定设备不一致")
	}
	now := s.now().UTC().Truncate(time.Millisecond)
	var result ClientLicenseResult
	err = store.RunInTx(ctx, s.Store.DB, func(ctx context.Context, tx bun.Tx) error {
		licenseRow := new(model.License)
		if err := tx.NewSelect().Model(licenseRow).Where("id=? AND fulfillment_mode='offline_file'", payload.LicenseID).For("UPDATE").Scan(ctx); err != nil {
			return err
		}
		if licenseRow.CustomerID != payload.CustomerID || licenseRow.ProductID == "" {
			return clientFailure("LICENSE_UNAVAILABLE", "此授权暂不可同步，请联系服务方")
		}
		if payload.Revision > licenseRow.CurrentRevision {
			return clientFailure("LICENSE_REVISION_AHEAD", "本机授权版本高于服务记录，请保留资料并联系服务方")
		}
		var productCode string
		if err := tx.NewRaw("SELECT license_product_code FROM products WHERE id=?", licenseRow.ProductID).Scan(ctx, &productCode); err != nil {
			return err
		}
		if productCode != offline.ProductID {
			return clientFailure("LICENSE_WRONG_PRODUCT", "授权不属于此产品")
		}
		var source model.LicenseIssuance
		if err := tx.NewSelect().Model(&source).Where("id=? AND license_id=? AND revision=?", payload.IssuanceID, payload.LicenseID, payload.Revision).Scan(ctx); err != nil {
			return clientFailure("LICENSE_INVALID", "本机授权不是服务端签发原件")
		}
		if source.BindingID == "" || !bytes.Equal(source.RawBytes, raw) {
			return clientFailure("LICENSE_INVALID", "本机授权不是服务端签发原件")
		}
		binding := new(model.OfflineDeviceBinding)
		if err := tx.NewSelect().Model(binding).Where("id=? AND license_id=? AND device_type=? AND device_digest=?", source.BindingID, payload.LicenseID, input.Device.Type, input.Device.Digest).For("UPDATE").Scan(ctx); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return clientFailure("LICENSE_INVALID", "本机授权与服务端设备记录不一致")
			}
			return err
		}
		var latest model.LicenseIssuance
		if err := tx.NewSelect().Model(&latest).Where("license_id=? AND binding_id=?", payload.LicenseID, binding.ID).OrderExpr("revision DESC").Limit(1).Scan(ctx); err != nil {
			return err
		}
		latestPayload, verifyErr := offline.Verify(latest.RawBytes, public, offline.ProductID)
		latestHash := sha256.Sum256(latest.RawBytes)
		if verifyErr != nil || latestPayload.IssuanceID != latest.ID || latestPayload.LicenseID != licenseRow.ID || latestPayload.CustomerID != licenseRow.CustomerID || latestPayload.Revision != latest.Revision || latestPayload.Binding != input.Device || hex.EncodeToString(latestHash[:]) != latest.SHA256 {
			return clientFailure("LICENSE_ARCHIVE_DAMAGED", "授权记录校验失败，请联系服务方")
		}
		if _, err := tx.NewRaw("UPDATE offline_device_bindings SET device_label=CASE WHEN ?='' THEN device_label ELSE ? END,app_version=?,build_id=?,last_seen_at=? WHERE id=?", input.DeviceLabel, input.DeviceLabel, input.AppVersion, input.BuildID, now, binding.ID).Exec(ctx); err != nil {
			return err
		}
		if binding.SupersededAt != nil {
			if latest.Kind != "transfer" || latest.Revision < payload.Revision || latest.Revision > licenseRow.CurrentRevision || (latest.Revision == payload.Revision && (payload.BindingState != "transferred" || source.ID != latest.ID)) || latestPayload.BindingState != "transferred" || latest.PreviousIssuanceID == "" {
				return clientFailure("LICENSE_ARCHIVE_DAMAGED", "授权记录校验失败，请联系服务方")
			}
			var previous model.LicenseIssuance
			if err := tx.NewSelect().Model(&previous).Where("id=? AND license_id=? AND binding_id=?", latest.PreviousIssuanceID, licenseRow.ID, binding.ID).Scan(ctx); err != nil {
				return clientFailure("LICENSE_ARCHIVE_DAMAGED", "授权记录校验失败，请联系服务方")
			}
			previousPayload, verifyErr := offline.Verify(previous.RawBytes, public, offline.ProductID)
			previousHash := sha256.Sum256(previous.RawBytes)
			if verifyErr != nil || previousPayload.IssuanceID != previous.ID || previousPayload.Revision != previous.Revision || previousPayload.Revision >= latestPayload.Revision || previousPayload.BindingState != "active" || previousPayload.LicenseID != licenseRow.ID || previousPayload.CustomerID != licenseRow.CustomerID || previousPayload.Binding != input.Device || previousPayload.NotBefore != latestPayload.NotBefore || (previousPayload.ExpiresAt == nil) != (latestPayload.ExpiresAt == nil) || (previousPayload.ExpiresAt != nil && *previousPayload.ExpiresAt != *latestPayload.ExpiresAt) || hex.EncodeToString(previousHash[:]) != previous.SHA256 {
				return clientFailure("LICENSE_ARCHIVE_DAMAGED", "授权记录校验失败，请联系服务方")
			}
			result = clientLicenseResult(latest.RawBytes, licenseRow.ID, licenseRow.CustomerID, binding.ID, now)
			return nil
		}
		if payload.BindingState != "active" || latestPayload.BindingState != "active" {
			return clientFailure("LICENSE_INVALID", "本机授权与当前设备状态不一致")
		}
		if !licenseRow.IssuanceEnabled {
			return clientFailure("LICENSE_UNAVAILABLE", "此授权暂不可同步，请联系服务方")
		}
		customer := new(model.CommercialCustomer)
		if err := tx.NewSelect().Model(customer).Where("id=?", licenseRow.CustomerID).For("SHARE").Scan(ctx); err != nil {
			return err
		}
		if !customer.IssuanceEnabled {
			return clientFailure("LICENSE_UNAVAILABLE", "此授权暂不可同步，请联系服务方")
		}
		if latest.Revision != licenseRow.CurrentRevision {
			return clientFailure("LICENSE_REVISION_CONFLICT", "服务端尚未为本设备归档当前修订")
		}
		if latestPayload.NotBefore != licenseRow.ValidFrom.UTC().Format("2006-01-02T15:04:05.000Z") || (licenseRow.ValidUntil == nil) != (latestPayload.ExpiresAt == nil) || (licenseRow.ValidUntil != nil && *latestPayload.ExpiresAt != licenseRow.ValidUntil.UTC().Format("2006-01-02T15:04:05.000Z")) {
			return clientFailure("LICENSE_ARCHIVE_DAMAGED", "授权记录校验失败，请联系服务方")
		}
		result = clientLicenseResult(latest.RawBytes, licenseRow.ID, customer.ID, binding.ID, now)
		if len(input.RecoveryRequest) > 0 {
			recovery, err := offline.ParseRecoveryRequest(input.RecoveryRequest)
			if err != nil || recovery.Device != input.Device {
				return clientFailure("RECOVERY_REQUEST_INVALID", "恢复请求无效，请重新生成后重试")
			}
			operationHash := hashJSON(struct {
				LicenseID       string
				RawLicenseHash  string
				RecoveryRequest string
			}{licenseRow.ID, hashJSON(input.RawLicense), string(input.RecoveryRequest)})
			var recovered struct {
				Result ClientLicenseResult `json:"result"`
			}
			replayed, err := previousOperation(ctx, tx, recovery.RequestID, operationHash, &recovered.Result)
			if err != nil {
				return err
			}
			if replayed {
				result = recovered.Result
				return nil
			}
			if _, err = tx.NewRaw("INSERT INTO offline_actors(id,display_name) VALUES(?,?) ON CONFLICT(id) DO NOTHING", clientServiceActor, "客户端激活服务").Exec(ctx); err != nil {
				return err
			}
			signed, _, err := s.Keys.SignRecovery(recovery, store.NewID(), now)
			if err != nil {
				return err
			}
			result.RawRecovery = string(signed)
			if err = store.LocalAuditIn(ctx, tx, clientServiceActor, "offline-license-recovery", recovery.RequestID, "client-sync-sign", map[string]any{"licenseId": licenseRow.ID, "bindingId": binding.ID}); err != nil {
				return err
			}
			if err = recordClientOperation(ctx, tx, recovery.RequestID, operationHash, "client-recovery", clientServiceActor, result); err != nil {
				return err
			}
		}
		return nil
	})
	return result, err
}
