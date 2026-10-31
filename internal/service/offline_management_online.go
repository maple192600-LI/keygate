package service

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/tabloy/keygate/internal/model"
	"github.com/tabloy/keygate/internal/offline"
	"github.com/tabloy/keygate/internal/store"
	"github.com/uptrace/bun"
)

func (s *OfflineIssuanceService) renewDevices(ctx context.Context, actor string, command OfflineIssueCommand) (OfflineIssueResult, error) {
	if !offline.ValidUUID4(command.OperationID) {
		return OfflineIssueResult{}, offlineFailure("BAD_REQUEST", "续期操作号须为 UUID v4")
	}
	operationHash := hashJSON(command)
	var result OfflineIssueResult
	err := store.RunInTx(ctx, s.Store.DB, func(ctx context.Context, tx bun.Tx) error {
		if err := store.EnsureOfflineActor(ctx, tx, actor); err != nil {
			return err
		}
		if _, err := tx.NewRaw("SELECT pg_advisory_xact_lock(hashtextextended(?,0))", "snail-issuance:"+command.OperationID).Exec(ctx); err != nil {
			return err
		}
		replayed, err := previousOperation(ctx, tx, command.OperationID, operationHash, &result)
		if err != nil || replayed {
			if err == nil && replayed {
				result.Replayed = true
			}
			return err
		}
		base, err := s.verifyProof(command.OfflineIntent, command.PreviewBase)
		if err != nil {
			return err
		}
		data, err := s.load(ctx, tx, command.OfflineIntent, true)
		if err != nil {
			return err
		}
		if data.license.CurrentRevision == 0 {
			return clientFailure("LICENSE_UNACTIVATED", "此授权尚未激活，暂不能续期")
		}
		term, err := offline.CalculateTerm(base, "renew", command.Term, periodFor(data))
		if err != nil {
			return err
		}
		if term.ExpiresAt != nil {
			end, _ := offline.ParseUTC(*term.ExpiresAt)
			if !s.now().Before(end) {
				return clientFailure("LICENSE_EXPIRED", "续期期限已结束，请重新预览")
			}
		}
		bindings, err := currentBindings(ctx, tx, data.license.ID)
		if err != nil {
			return err
		}
		revision := data.license.CurrentRevision
		var currentIssuanceID string
		issuances := []OfflineIssuanceRef{}
		if len(bindings) > 0 {
			if revision >= 9007199254740991 {
				return clientFailure("LICENSE_REVISION_LIMIT", "授权版本超出支持范围，请联系服务方")
			}
			revision++
			period := term.Period
			for _, binding := range bindings {
				if binding.PreviousIssuanceID == "" {
					return errors.New("active device has no previous signed issuance")
				}
				rowHash := hashJSON(struct {
					OperationID, BindingID string
					Revision               int64
				}{command.OperationID, binding.ID, revision})
				row, _, err := appendIssuance(ctx, tx, s.Keys, actor, data.license, data.customer.ID, binding, revision, "renew", "active", rowHash, period, base)
				if err != nil {
					return err
				}
				currentIssuanceID = row.ID
				issuances = append(issuances, OfflineIssuanceRef{IssuanceID: row.ID, BindingID: binding.ID})
			}
			if len(issuances) > 0 {
				result.IssuanceID = issuances[len(issuances)-1].IssuanceID
			}
		}
		if err = store.AllowOfflineWriteIn(ctx, tx, "issue"); err != nil {
			return err
		}
		start, err := offline.ParseUTC(term.NotBefore)
		if err != nil {
			return err
		}
		var end *time.Time
		if term.ExpiresAt != nil {
			parsed, parseErr := offline.ParseUTC(*term.ExpiresAt)
			if parseErr != nil {
				return parseErr
			}
			end = &parsed
		}
		var endDate *time.Time
		if command.Term.Kind == "date" {
			parsed, parseErr := time.Parse("2006-01-02", command.Term.EndDate)
			if parseErr != nil || parsed.Format("2006-01-02") != command.Term.EndDate {
				return offlineFailure("BAD_REQUEST", "请选择有效的截止日期")
			}
			endDate = &parsed
		}
		if _, err = tx.NewRaw(`UPDATE licenses SET current_revision=?,current_issuance_id=CASE WHEN ?='' THEN current_issuance_id ELSE ? END,
valid_from=?,valid_until=?,term_kind=?,term_amount=?,term_end_date=?,status='active',updated_at=now() WHERE id=?`, revision, currentIssuanceID, currentIssuanceID, start, end, command.Term.Kind, command.Term.Amount, endDate, data.license.ID).Exec(ctx); err != nil {
			return err
		}
		result.LicenseID = data.license.ID
		result.CustomerNumber = "C-" + strings.ToUpper(strings.ReplaceAll(data.customer.ID, "-", ""))
		result.LicenseNumber = "L-" + strings.ToUpper(strings.ReplaceAll(data.license.ID, "-", ""))
		result.DisplayNumber = data.license.DisplayNumber
		result.DeviceLimit = data.license.DeviceLimit
		result.Issuances = issuances
		result.Revision = revision
		result.NotBefore = term.NotBefore
		result.ExpiresAt = term.ExpiresAt
		if err = store.LocalAuditIn(ctx, tx, actor, "offline-license", data.license.ID, "renew", map[string]any{"revision": revision, "issuanceCount": len(issuances), "expiresAt": term.ExpiresAt}); err != nil {
			return err
		}
		return recordClientOperation(ctx, tx, command.OperationID, operationHash, "renew-license", actor, result)
	})
	return result, err
}

func (s *OfflineIssuanceService) releaseBinding(ctx context.Context, actor string, command OfflineIssueCommand) (OfflineIssueResult, error) {
	if !offline.ValidUUID4(command.OperationID) {
		return OfflineIssueResult{}, offlineFailure("BAD_REQUEST", "换机操作号须为 UUID v4")
	}
	operationHash := hashJSON(command)
	var result OfflineIssueResult
	err := store.RunInTx(ctx, s.Store.DB, func(ctx context.Context, tx bun.Tx) error {
		if err := store.EnsureOfflineActor(ctx, tx, actor); err != nil {
			return err
		}
		if _, err := tx.NewRaw("SELECT pg_advisory_xact_lock(hashtextextended(?,0))", "snail-issuance:"+command.OperationID).Exec(ctx); err != nil {
			return err
		}
		replayed, err := previousOperation(ctx, tx, command.OperationID, operationHash, &result)
		if err != nil || replayed {
			if err == nil && replayed {
				result.Replayed = true
			}
			return err
		}
		base, err := s.verifyProof(command.OfflineIntent, command.PreviewBase)
		if err != nil {
			return err
		}
		licenseRow := new(model.License)
		if err = tx.NewSelect().Model(licenseRow).Where("id=? AND fulfillment_mode='offline_file'", command.LicenseID).For("UPDATE").Scan(ctx); err != nil {
			return err
		}
		if licenseRow.CurrentRevision != command.ExpectedRevision {
			return offlineFailure("REVISION_CONFLICT", "授权已有变化，请刷新并核对当前设备")
		}
		if licenseRow.CurrentRevision >= 9007199254740991 {
			return offlineFailure("LICENSE_REVISION_LIMIT", "授权版本超出支持范围，请联系服务方")
		}
		binding := new(model.OfflineDeviceBinding)
		if err = tx.NewSelect().Model(binding).Where("id=? AND license_id=? AND superseded_at IS NULL", command.BindingID, licenseRow.ID).For("UPDATE").Scan(ctx); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return clientFailure("BINDING_NOT_CURRENT", "所选设备已解除绑定，请刷新后核对")
			}
			return err
		}
		bindings, err := currentBindings(ctx, tx, licenseRow.ID)
		if err != nil {
			return err
		}
		transferred := -1
		for i := range bindings {
			if bindings[i].ID == binding.ID {
				transferred = i
			}
		}
		if transferred < 0 {
			return clientFailure("BINDING_NOT_CURRENT", "所选设备已解除绑定，请刷新后核对")
		}
		now := base.UTC().Truncate(time.Millisecond)
		revision := licenseRow.CurrentRevision + 1
		period := periodForLicense(licenseRow)
		issuances := make([]OfflineIssuanceRef, 0, len(bindings))
		currentIssuanceID := ""
		for i, current := range bindings {
			if current.PreviousIssuanceID == "" {
				return errors.New("active device has no previous signed issuance")
			}
			bindingState := "active"
			if i == transferred {
				bindingState = "transferred"
			}
			rowHash := hashJSON(struct {
				OperationID, BindingID string
				Revision               int64
				BindingState           string
			}{command.OperationID, current.ID, revision, bindingState})
			row, _, issueErr := appendIssuance(ctx, tx, s.Keys, actor, licenseRow, licenseRow.CustomerID, current, revision, "transfer", bindingState, rowHash, period, now)
			if issueErr != nil {
				return issueErr
			}
			issuances = append(issuances, OfflineIssuanceRef{IssuanceID: row.ID, BindingID: current.ID})
			if i != transferred {
				currentIssuanceID = row.ID
			}
		}
		if _, err = tx.NewRaw("UPDATE offline_device_bindings SET superseded_at=? WHERE id=? AND superseded_at IS NULL", now, binding.ID).Exec(ctx); err != nil {
			return err
		}
		if currentIssuanceID == "" {
			currentIssuanceID = issuances[transferred].IssuanceID
		}
		if err = store.AllowOfflineWriteIn(ctx, tx, "issue"); err != nil {
			return err
		}
		if _, err = tx.NewRaw("UPDATE licenses SET current_revision=?,current_issuance_id=?,updated_at=now() WHERE id=?", revision, currentIssuanceID, licenseRow.ID).Exec(ctx); err != nil {
			return err
		}
		var active int
		if err = tx.NewRaw("SELECT count(*) FROM offline_device_bindings WHERE license_id=? AND superseded_at IS NULL", licenseRow.ID).Scan(ctx, &active); err != nil {
			return err
		}
		result = OfflineIssueResult{LicenseID: licenseRow.ID, LicenseNumber: "L-" + strings.ToUpper(strings.ReplaceAll(licenseRow.ID, "-", "")), DisplayNumber: licenseRow.DisplayNumber, DeviceLimit: licenseRow.DeviceLimit, Issuances: issuances, IssuanceID: currentIssuanceID, Revision: revision}
		if err = tx.NewRaw("SELECT 'C-'||upper(replace(customer_id,'-','')) FROM licenses WHERE id=?", licenseRow.ID).Scan(ctx, &result.CustomerNumber); err != nil {
			return err
		}
		result.ActiveDevices = active
		result.NotBefore = period.NotBefore
		result.ExpiresAt = period.ExpiresAt
		if err = store.LocalAuditIn(ctx, tx, actor, "offline-license", licenseRow.ID, "transfer", map[string]any{"bindingId": binding.ID, "activeDevices": active, "revision": revision, "reason": command.Reason}); err != nil {
			return err
		}
		return recordClientOperation(ctx, tx, command.OperationID, operationHash, "release-device-binding", actor, result)
	})
	return result, err
}
