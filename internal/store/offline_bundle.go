package store

// This is a versioned, local business transfer format, not a database backup.
// Only this fixed table/column vocabulary can ever reach SQL identifiers.
import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/tabloy/keygate/internal/license"
	"github.com/tabloy/keygate/internal/model"
	"github.com/tabloy/keygate/internal/offline"
	"github.com/uptrace/bun"
)

const BundleLimit = 128 * 1024 * 1024

type BundleRow map[string]json.RawMessage
type BusinessBundle struct {
	FormatVersion  int                    `json:"formatVersion"`
	Kind           string                 `json:"kind"`
	ProductCode    string                 `json:"productCode"`
	CreatedAt      string                 `json:"createdAt"`
	PublicIdentity offline.PublicIdentity `json:"publicIdentity"`
	Tables         map[string][]BundleRow `json:"tables"`
}
type BundleMapping struct {
	Kind     string `json:"kind"`
	Code     string `json:"code"`
	SourceID string `json:"sourceId"`
	TargetID string `json:"targetId"`
}
type BundleSummary struct {
	Customers int             `json:"customers"`
	Licenses  int             `json:"licenses"`
	Originals int             `json:"originals"`
	KeyID     string          `json:"keyId"`
	Mappings  []BundleMapping `json:"mappings"`
}

var bundleTables = []string{"products", "plans", "entitlements", "offline_actors", "commercial_customers", "offline_requests", "licenses", "offline_device_bindings", "license_issuances", "license_deliveries", "offline_management_operations", "audit_logs"}

const bundleLicenseColumns = "id,product_id,plan_id,email,customer_id,fulfillment_mode,issuance_enabled,current_revision,current_issuance_id,display_number,status,valid_from,valid_until,notes,org_name,created_at,updated_at"

func bundleError(message string) error {
	return &offline.Error{Code: "BUNDLE_INVALID", Message: message}
}
func rowText(row BundleRow, key string) string {
	var s string
	_ = json.Unmarshal(row[key], &s)
	return s
}
func rowInt(row BundleRow, key string) int64      { var n int64; _ = json.Unmarshal(row[key], &n); return n }
func rowSet(row BundleRow, key string, value any) { raw, _ := json.Marshal(value); row[key] = raw }
func rowBytes(row BundleRow, key string) ([]byte, error) {
	s := rowText(row, key)
	if !strings.HasPrefix(s, `\x`) {
		return nil, bundleError("原件编码无效")
	}
	return hex.DecodeString(s[2:])
}
func readBundleRows(ctx context.Context, db bun.IDB, table, projection, condition, order string) ([]BundleRow, error) {
	var texts []string
	if err := db.NewRaw("SELECT row_to_json(r)::text FROM (SELECT "+projection+" FROM "+table+" "+condition+" ORDER BY "+order+") r").Scan(ctx, &texts); err != nil {
		return nil, err
	}
	rows := make([]BundleRow, 0, len(texts))
	for _, v := range texts {
		var row BundleRow
		if err := json.Unmarshal([]byte(v), &row); err != nil {
			return nil, err
		}
		rows = append(rows, row)
	}
	return rows, nil
}
func exportBusinessIn(ctx context.Context, db bun.IDB, public offline.PublicIdentity) (*BusinessBundle, error) {
	for _, table := range []string{"activations", "subscriptions", "usage_events", "usage_counters", "floating_sessions", "seats", "license_addons", "metered_billing", "notifications", "license_renewals"} {
		var onlineRecords int
		if err := db.NewRaw("SELECT count(*) FROM "+table+" r JOIN licenses l ON l.id=r.license_id WHERE l.fulfillment_mode='offline_file'").Scan(ctx, &onlineRecords); err != nil {
			return nil, err
		}
		if onlineRecords != 0 {
			return nil, bundleError("离线授权存在不应有的在线生命周期记录")
		}
	}
	b := &BusinessBundle{1, "snail-commercial-bundle", offline.ProductID, time.Now().UTC().Format(time.RFC3339Nano), public, map[string][]BundleRow{}}
	b.PublicIdentity.State = "public"
	for _, table := range bundleTables {
		projection, condition, order := "*", "", "id"
		switch table {
		case "products":
			condition = "WHERE license_product_code='cn.snailfinance.desktop'"
		case "plans":
			condition = "WHERE product_id IN (SELECT id FROM products WHERE license_product_code='cn.snailfinance.desktop')"
		case "entitlements":
			condition = "WHERE plan_id IN (SELECT p.id FROM plans p JOIN products d ON d.id=p.product_id WHERE d.license_product_code='cn.snailfinance.desktop')"
		case "licenses":
			projection = bundleLicenseColumns
			condition = "WHERE fulfillment_mode='offline_file'"
		case "offline_requests":
			order = "request_id"
		case "offline_management_operations":
			order = "operation_id"
		case "license_issuances":
			order = "license_id,revision"
		case "audit_logs":
			condition = "WHERE actor_id IN (SELECT id FROM offline_actors)"
			order = "created_at,id"
		}
		rows, err := readBundleRows(ctx, db, table, projection, condition, order)
		if err != nil {
			return nil, err
		}
		b.Tables[table] = rows
	}
	if err := ValidateBusinessBundle(b); err != nil {
		return nil, err
	}
	return b, nil
}
func (s *Store) ExportBusinessBundle(ctx context.Context, public offline.PublicIdentity) ([]byte, error) {
	var b *BusinessBundle
	err := s.DB.RunInTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true}, func(ctx context.Context, tx bun.Tx) error {
		var err error
		b, err = exportBusinessIn(ctx, tx, public)
		return err
	})
	if err != nil {
		return nil, err
	}
	return encodeBusinessBundle(b)
}
func encodeBusinessBundle(b *BusinessBundle) ([]byte, error) {
	raw, err := json.Marshal(b)
	if err != nil {
		return nil, err
	}
	if len(raw) > BundleLimit {
		return nil, &offline.Error{Code: "BUNDLE_TOO_LARGE", Message: "管理移交包超过本期本地文件大小限制"}
	}
	return raw, nil
}
func ParseBusinessBundle(raw []byte) (*BusinessBundle, error) {
	if _, err := offline.StrictJSON(raw, BundleLimit); err != nil {
		return nil, err
	}
	var b BusinessBundle
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&b); err != nil {
		return nil, bundleError("管理数据包结构无效")
	}
	if err := ValidateBusinessBundle(&b); err != nil {
		return nil, err
	}
	return &b, nil
}
func ValidateBusinessBundle(b *BusinessBundle) error {
	if b.FormatVersion != 1 || b.Kind != "snail-commercial-bundle" || b.ProductCode != offline.ProductID || len(b.Tables) != len(bundleTables) {
		return bundleError("管理数据包版本或产品不受支持")
	}
	for _, table := range bundleTables {
		if b.Tables[table] == nil {
			return bundleError("管理数据包缺少完整表：" + table)
		}
	}
	var trusted map[string]ed25519.PublicKey
	if b.PublicIdentity.KeyID != "" {
		pub, err := offline.ParsePublic([]byte(b.PublicIdentity.PublicKeyPEM))
		if err != nil || offline.KeyID(pub) != b.PublicIdentity.KeyID || b.PublicIdentity.KeyID == offline.TestKeyID {
			return bundleError("签发公钥身份无效")
		}
		trusted = map[string]ed25519.PublicKey{b.PublicIdentity.KeyID: pub}
	}
	indexes := map[string]map[string]BundleRow{}
	for _, table := range bundleTables {
		indexes[table] = map[string]BundleRow{}
		key := "id"
		if table == "offline_requests" {
			key = "request_id"
		}
		if table == "offline_management_operations" {
			key = "operation_id"
		}
		for _, row := range b.Tables[table] {
			id := rowText(row, key)
			serviceActor := table == "offline_actors" && (id == SigningServiceActorID || id == ClientActivationServiceActorID)
			if (!offline.ValidUUID4(id) && !serviceActor) || indexes[table][id] != nil {
				return bundleError("记录编号无效或重复：" + table)
			}
			indexes[table][id] = row
		}
	}
	for _, r := range b.Tables["offline_requests"] {
		raw, err := rowBytes(r, "raw_bytes")
		if err != nil {
			return err
		}
		req, fp, err := offline.ParseRequest(raw, offline.ProductID)
		if err != nil || req.RequestID != rowText(r, "request_id") || fp != rowText(r, "fingerprint") || req.Device.Digest != rowText(r, "device_digest") || req.Device.Type != rowText(r, "device_type") || req.ProductID != rowText(r, "product_code") || req.DeviceLabel != rowText(r, "device_label") {
			return bundleError("申请原件与登记不一致")
		}
	}
	highest := map[string]int64{}
	for _, r := range b.Tables["license_issuances"] {
		raw, err := rowBytes(r, "raw_bytes")
		if err != nil {
			return err
		}
		sum := sha256.Sum256(raw)
		if hex.EncodeToString(sum[:]) != rowText(r, "sha256") {
			return bundleError("授权原件摘要不一致")
		}
		p, err := offline.Verify(raw, trusted, offline.ProductID)
		if err != nil {
			return err
		}
		l := indexes["licenses"][p.LicenseID]
		binding := indexes["offline_device_bindings"][rowText(r, "binding_id")]
		req := indexes["offline_requests"][rowText(r, "request_id")]
		if l == nil || binding == nil || req == nil || p.IssuanceID != rowText(r, "id") || p.LicenseID != rowText(r, "license_id") || p.CustomerID != rowText(l, "customer_id") || p.Revision != rowInt(r, "revision") || p.KeyID != rowText(r, "key_id") || p.Binding.Digest != rowText(binding, "device_digest") || p.Binding.Type != rowText(binding, "device_type") || p.Binding.Digest != rowText(req, "device_digest") || rowText(binding, "license_id") != p.LicenseID || !sameBundleTime(r["issued_at"], p.IssuedAt) || !sameBundleTime(r["not_before"], p.NotBefore) || !sameNullableBundleTime(r["expires_at"], p.ExpiresAt) {
			return bundleError("授权原件与客户、设备或版本登记不一致")
		}
		prev := rowText(r, "previous_issuance_id")
		if p.Revision == 1 {
			if prev != "" || rowText(r, "kind") != "initial" {
				return bundleError("首版授权关系无效")
			}
		} else {
			pr := indexes["license_issuances"][prev]
			if pr == nil || rowText(pr, "license_id") != p.LicenseID || rowInt(pr, "revision") != p.Revision-1 {
				return bundleError("签发版本链不完整")
			}
			kind := rowText(r, "kind")
			sameBinding := rowText(pr, "binding_id") == rowText(r, "binding_id")
			if (kind == "renew" && !sameBinding) || (kind == "transfer" && sameBinding) || (kind != "renew" && kind != "transfer") {
				return bundleError("续期或换机设备关系无效")
			}
		}
		if p.Revision > highest[p.LicenseID] {
			highest[p.LicenseID] = p.Revision
		}
	}
	for _, l := range b.Tables["licenses"] {
		id := rowText(l, "id")
		plan := indexes["plans"][rowText(l, "plan_id")]
		product := indexes["products"][rowText(l, "product_id")]
		if rowText(l, "fulfillment_mode") != "offline_file" || indexes["commercial_customers"][rowText(l, "customer_id")] == nil || plan == nil || product == nil || rowText(plan, "product_id") != rowText(l, "product_id") || rowText(product, "license_product_code") != offline.ProductID {
			return bundleError("授权产品与计划关系无效")
		}
		currentRevision := rowInt(l, "current_revision")
		issuanceID := rowText(l, "current_issuance_id")
		current := indexes["license_issuances"][issuanceID]
		active, bindings := 0, 0
		for _, binding := range b.Tables["offline_device_bindings"] {
			if rowText(binding, "license_id") == id {
				bindings++
				if string(binding["superseded_at"]) == "null" {
					active++
					if current != nil && rowText(binding, "id") != rowText(current, "binding_id") {
						return bundleError("当前设备与最新签发原件不一致")
					}
				}
			}
		}
		if currentRevision == 0 {
			// valid_from is a required legacy column; revision zero means it is only a creation placeholder.
			if issuanceID != "" || highest[id] != 0 || string(l["valid_until"]) != "null" || bindings != 0 {
				return bundleError("未激活授权不得包含签发版本或设备绑定")
			}
			continue
		}
		if current == nil || highest[id] != currentRevision || rowInt(current, "revision") != highest[id] || rowText(current, "license_id") != id || !equalBundleTime(l["valid_from"], current["not_before"]) || !equalBundleTime(l["valid_until"], current["expires_at"]) {
			return bundleError("当前授权指针或最高版本不一致")
		}
		if active != 1 {
			return bundleError("授权没有唯一当前设备")
		}
	}
	for _, binding := range b.Tables["offline_device_bindings"] {
		if indexes["licenses"][rowText(binding, "license_id")] == nil {
			return bundleError("设备缺少所属授权")
		}
		if id := rowText(binding, "previous_binding_id"); id != "" {
			previous := indexes["offline_device_bindings"][id]
			if previous == nil || rowText(previous, "license_id") != rowText(binding, "license_id") || string(previous["superseded_at"]) == "null" {
				return bundleError("设备历史关系无效")
			}
		}
	}
	return nil
}
func sameBundleTime(raw json.RawMessage, expected string) bool {
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return false
	}
	a, e := time.Parse(time.RFC3339Nano, s)
	b, f := time.Parse(time.RFC3339Nano, expected)
	return e == nil && f == nil && a.Equal(b)
}
func sameNullableBundleTime(raw json.RawMessage, expected *string) bool {
	if expected == nil {
		return string(raw) == "null"
	}
	return sameBundleTime(raw, *expected)
}
func equalBundleTime(a, b json.RawMessage) bool {
	if string(a) == "null" || string(b) == "null" {
		return string(a) == string(b)
	}
	var s string
	if json.Unmarshal(b, &s) != nil {
		return false
	}
	return sameBundleTime(a, s)
}
func bundleColumns(ctx context.Context, tx bun.IDB, table string) ([]string, error) {
	var cols []string
	err := tx.NewRaw("SELECT column_name FROM information_schema.columns WHERE table_schema='public' AND table_name=? ORDER BY ordinal_position", table).Scan(ctx, &cols)
	return cols, err
}
func insertBundleRow(ctx context.Context, tx bun.IDB, table string, row BundleRow, columns []string) error {
	if len(row) != len(columns) {
		return bundleError("记录字段与当前版本不一致：" + table)
	}
	for _, c := range columns {
		if _, ok := row[c]; !ok {
			return bundleError("记录缺少字段：" + table + "." + c)
		}
	}
	raw, err := json.Marshal(row)
	if err != nil {
		return err
	}
	quoted := make([]string, len(columns))
	for i, c := range columns {
		quoted[i] = `"` + c + `"`
	}
	names := strings.Join(quoted, ",")
	_, err = tx.NewRaw("INSERT INTO "+table+" ("+names+") SELECT "+names+" FROM json_populate_record(NULL::"+table+",?::json)", string(raw)).Exec(ctx)
	return err
}
func sameDefinition(a, b BundleRow, exclude ...string) bool {
	aa, bb := map[string]any{}, map[string]any{}
	excluded := map[string]bool{}
	for _, k := range exclude {
		excluded[k] = true
	}
	for k, v := range a {
		if !excluded[k] {
			var x any
			_ = json.Unmarshal(v, &x)
			aa[k] = x
		}
	}
	for k, v := range b {
		if !excluded[k] {
			var x any
			_ = json.Unmarshal(v, &x)
			bb[k] = x
		}
	}
	return reflect.DeepEqual(aa, bb)
}
func (s *Store) ImportBusinessBundle(ctx context.Context, b *BusinessBundle, actor string, commit bool) (BundleSummary, error) {
	summary := BundleSummary{Customers: len(b.Tables["commercial_customers"]), Licenses: len(b.Tables["licenses"]), Originals: len(b.Tables["license_issuances"]), KeyID: b.PublicIdentity.KeyID, Mappings: []BundleMapping{}}
	if err := ValidateBusinessBundle(b); err != nil {
		return summary, err
	}
	if s.LicenseKeyAEAD == nil {
		return summary, bundleError("本机加密配置不可用")
	}
	if _, err := s.ExpectedSigningIdentity(ctx, b.PublicIdentity); err != nil {
		return summary, err
	}
	previewRollback := fmt.Errorf("business preview complete")
	err := s.DB.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		if err := AllowOfflineWriteIn(ctx, tx, "bundle"); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock(8675310)"); err != nil {
			return err
		}
		for _, table := range []string{"commercial_customers", "offline_requests", "licenses", "offline_device_bindings", "license_issuances", "license_deliveries", "offline_management_operations"} {
			var n int
			if err := tx.NewRaw("SELECT count(*) FROM "+table).Scan(ctx, &n); err != nil {
				return err
			}
			if n != 0 {
				return &offline.Error{Code: "BUNDLE_TARGET_NOT_EMPTY", Message: "商业资料不为空，不能合并导入；现有资料保留"}
			}
		}
		productMap, planMap := map[string]string{}, map[string]string{}
		for _, p := range b.Tables["products"] {
			code := rowText(p, "license_product_code")
			if code != offline.ProductID {
				return bundleError("产品合同代码不受支持")
			}
			rows, err := readBundleRows(ctx, tx, "products", "*", "WHERE license_product_code='cn.snailfinance.desktop'", "id")
			if err != nil {
				return err
			}
			if len(rows) != 1 || !sameDefinition(p, rows[0], "id", "created_at") {
				return bundleError("目标产品定义与数据包冲突")
			}
			source, target := rowText(p, "id"), rowText(rows[0], "id")
			productMap[source] = target
			summary.Mappings = append(summary.Mappings, BundleMapping{"product", code, source, target})
		}
		for _, p := range b.Tables["plans"] {
			targetProduct := productMap[rowText(p, "product_id")]
			if targetProduct == "" {
				return bundleError("计划缺少产品映射")
			}
			var texts []string
			if err := tx.NewRaw("SELECT row_to_json(p)::text FROM plans p WHERE product_id=? AND slug=?", targetProduct, rowText(p, "slug")).Scan(ctx, &texts); err != nil {
				return err
			}
			if len(texts) != 1 {
				return bundleError("目标计划缺少固定 slug 映射")
			}
			var target BundleRow
			_ = json.Unmarshal([]byte(texts[0]), &target)
			if !sameDefinition(p, target, "id", "product_id", "checkout_id", "created_at") {
				return bundleError("计划定义冲突，不能按名称猜测")
			}
			source, id := rowText(p, "id"), rowText(target, "id")
			planMap[source] = id
			summary.Mappings = append(summary.Mappings, BundleMapping{"plan", rowText(p, "slug"), source, id})
		}
		// Plans already exist in an initialized destination. Their entitlements must match too.
		for source, target := range planMap {
			var rows []BundleRow
			for _, r := range b.Tables["entitlements"] {
				if rowText(r, "plan_id") == source {
					rows = append(rows, r)
				}
			}
			existing, err := readBundleRows(ctx, tx, "entitlements", "*", "WHERE plan_id='"+target+"'", "feature")
			if err != nil {
				return err
			}
			sort.Slice(rows, func(i, j int) bool { return rowText(rows[i], "feature") < rowText(rows[j], "feature") })
			if len(rows) != len(existing) {
				return bundleError("计划权益定义冲突")
			}
			for i := range rows {
				if !sameDefinition(rows[i], existing[i], "id", "plan_id") {
					return bundleError("计划权益定义冲突")
				}
			}
		}
		for _, table := range bundleTables[3:] {
			cols, err := bundleColumns(ctx, tx, table)
			if err != nil {
				return err
			}
			rows := b.Tables[table]
			if table == "licenses" {
				cols = append(strings.Split(bundleLicenseColumns, ","), "license_key", "key_hash", "license_key_encrypted")
			}
			if table == "offline_device_bindings" {
				rows, err = orderBindings(rows)
				if err != nil {
					return err
				}
			}
			if table == "license_issuances" {
				rows = append([]BundleRow{}, rows...)
				sort.Slice(rows, func(i, j int) bool {
					a, c := rowText(rows[i], "license_id"), rowText(rows[j], "license_id")
					if a == c {
						return rowInt(rows[i], "revision") < rowInt(rows[j], "revision")
					}
					return a < c
				})
			}
			for _, source := range rows {
				row := BundleRow{}
				for k, v := range source {
					row[k] = v
				}
				if table == "licenses" {
					p, ok := productMap[rowText(row, "product_id")]
					q, qok := planMap[rowText(row, "plan_id")]
					if !ok || !qok {
						return bundleError("授权缺少产品或计划映射")
					}
					rowSet(row, "product_id", p)
					rowSet(row, "plan_id", q)
					l := &model.License{ID: rowText(row, "id"), LicenseKey: license.GenerateKey("KG")}
					if err := s.prepareLicenseForInsert(l); err != nil {
						return err
					}
					rowSet(row, "license_key", l.LicenseKey)
					rowSet(row, "key_hash", l.KeyHash)
					rowSet(row, "license_key_encrypted", `\x`+hex.EncodeToString(l.LicenseKeyEncrypted))
				}
				if table == "offline_actors" {
					var n int
					if err := tx.NewRaw("SELECT count(*) FROM offline_actors WHERE id=?", rowText(row, "id")).Scan(ctx, &n); err != nil {
						return err
					}
					if n != 0 {
						return bundleError("历史操作者编号与本机已有记录冲突")
					}
				}
				if err := insertBundleRow(ctx, tx, table, row, cols); err != nil {
					return err
				}
			}
		}
		if _, err := tx.ExecContext(ctx, "SET CONSTRAINTS ALL IMMEDIATE"); err != nil {
			return err
		}
		if _, err := exportBusinessIn(ctx, tx, b.PublicIdentity); err != nil {
			return err
		}
		// Display identifiers are preserved. The next generated identifier must not reuse their numeric suffix.
		if !commit {
			return previewRollback
		}
		if _, err := tx.ExecContext(ctx, `SELECT setval('snail_license_display_seq',GREATEST((SELECT last_value FROM snail_license_display_seq),COALESCE((SELECT max(substring(display_number from '[0-9]+$')::bigint) FROM licenses WHERE fulfillment_mode='offline_file'),0),1))`); err != nil {
			return err
		}
		identityRaw, _ := json.Marshal(b.PublicIdentity)
		if _, err := tx.ExecContext(ctx, "INSERT INTO settings(key,value) VALUES('snail_public_identity',?) ON CONFLICT(key) DO UPDATE SET value=EXCLUDED.value", string(identityRaw)); err != nil {
			return err
		}
		return LocalAuditIn(ctx, tx, actor, "business-bundle", "", "import", map[string]any{"customers": summary.Customers, "licenses": summary.Licenses, "originals": summary.Originals})
	})
	if err == previewRollback {
		return summary, nil
	}
	return summary, err
}
func orderBindings(rows []BundleRow) ([]BundleRow, error) {
	done := map[string]bool{}
	result := make([]BundleRow, 0, len(rows))
	for len(result) < len(rows) {
		progress := false
		for _, r := range rows {
			id := rowText(r, "id")
			if done[id] {
				continue
			}
			prev := rowText(r, "previous_binding_id")
			if prev == "" || done[prev] {
				result = append(result, r)
				done[id] = true
				progress = true
			}
		}
		if !progress {
			return nil, bundleError("设备历史关系不完整或成环")
		}
	}
	return result, nil
}
