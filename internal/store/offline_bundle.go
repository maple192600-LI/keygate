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

const bundleLicenseColumns = "id,product_id,plan_id,email,customer_id,fulfillment_mode,issuance_enabled,current_revision,current_issuance_id,display_number,status,device_limit,term_kind,term_amount,term_end_date,term_starts_at,valid_from,valid_until,notes,org_name,created_at,updated_at"

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
	type issuanceRecord struct {
		row     BundleRow
		payload offline.Payload
	}
	highest := map[string]int64{}
	revisions := map[string]map[int64]struct{}{}
	seenBindingRevision := map[string]map[int64]map[string]bool{}
	issuancesByID := map[string]issuanceRecord{}
	issuancesByBinding := map[string][]issuanceRecord{}
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
		if p.Revision < 1 || p.Revision > 9007199254740991 || !bundleIssuanceStateValid(rowText(r, "kind"), p.BindingState) {
			return bundleError("签发动作或设备状态无效")
		}
		bindingID := rowText(r, "binding_id")
		if revisions[p.LicenseID] == nil {
			revisions[p.LicenseID] = map[int64]struct{}{}
			seenBindingRevision[p.LicenseID] = map[int64]map[string]bool{}
		}
		if seenBindingRevision[p.LicenseID][p.Revision] == nil {
			seenBindingRevision[p.LicenseID][p.Revision] = map[string]bool{}
		}
		if seenBindingRevision[p.LicenseID][p.Revision][bindingID] {
			return bundleError("同一设备存在重复修订原件")
		}
		seenBindingRevision[p.LicenseID][p.Revision][bindingID] = true
		revisions[p.LicenseID][p.Revision] = struct{}{}
		record := issuanceRecord{row: r, payload: p}
		issuancesByID[p.IssuanceID] = record
		issuancesByBinding[bindingID] = append(issuancesByBinding[bindingID], record)
		if p.Revision > highest[p.LicenseID] {
			highest[p.LicenseID] = p.Revision
		}
	}
	latestByBinding := map[string]issuanceRecord{}
	legacyTransferParents := map[string]string{}
	legacyTransferChildren := map[string]int{}
	for bindingID, history := range issuancesByBinding {
		sort.Slice(history, func(i, j int) bool {
			return rowInt(history[i].row, "revision") < rowInt(history[j].row, "revision")
		})
		binding := indexes["offline_device_bindings"][bindingID]
		if binding == nil {
			return bundleError("签发原件缺少设备绑定")
		}
		for i, record := range history {
			prevID := rowText(record.row, "previous_issuance_id")
			kind, state := rowText(record.row, "kind"), record.payload.BindingState
			if i > 0 && kind == "initial" {
				return bundleError("首份授权原件不能重复出现")
			}
			if i == 0 {
				if prevID == "" {
					if (rowInt(record.row, "revision") == 1 && kind != "initial") || (rowInt(record.row, "revision") > 1 && kind != "activate") {
						return bundleError("设备首份授权原件关系无效")
					}
					continue
				}
				previous, exists := issuancesByID[prevID]
				previousBindingID := rowText(binding, "previous_binding_id")
				if !exists || kind != "transfer" || state != "active" || previous.payload.BindingState != "active" || rowText(previous.row, "license_id") != rowText(record.row, "license_id") || rowInt(record.row, "revision") != rowInt(previous.row, "revision")+1 || previousBindingID == "" || previousBindingID == bindingID || previousBindingID != rowText(previous.row, "binding_id") {
					return bundleError("跨设备换机原件关系无效")
				}
				legacyTransferParents[bindingID] = previousBindingID
				legacyTransferChildren[previousBindingID]++
				continue
			}
			previous := history[i-1]
			if prevID != rowText(previous.row, "id") || rowInt(record.row, "revision") != rowInt(previous.row, "revision")+1 || previous.payload.BindingState != "active" {
				return bundleError("设备签发历史不连续")
			}
		}
		latestByBinding[bindingID] = history[len(history)-1]
	}
	for childID, parentID := range legacyTransferParents {
		childHistory := issuancesByBinding[childID]
		parentLatest, exists := latestByBinding[parentID]
		if !exists || len(childHistory) == 0 || rowText(childHistory[0].row, "previous_issuance_id") != rowText(parentLatest.row, "id") {
			return bundleError("跨设备换机原件未指向前一设备的最新版本")
		}
	}
	for _, binding := range b.Tables["offline_device_bindings"] {
		id := rowText(binding, "id")
		previousID := rowText(binding, "previous_binding_id")
		if previousID != "" {
			previous := indexes["offline_device_bindings"][previousID]
			if previous == nil || previousID == id || rowText(previous, "license_id") != rowText(binding, "license_id") || string(previous["superseded_at"]) == "null" || legacyTransferParents[id] != previousID || legacyTransferChildren[previousID] != 1 {
				return bundleError("设备历史关系无效")
			}
		}
		if _, exists := latestByBinding[id]; !exists {
			return bundleError("设备绑定缺少签发原件")
		}
		latest := latestByBinding[id]
		superseded := string(binding["superseded_at"]) != "null"
		if (!superseded && latest.payload.BindingState != "active") || (superseded && latest.payload.BindingState == "active" && legacyTransferChildren[id] != 1) || (superseded && latest.payload.BindingState == "transferred" && rowText(latest.row, "kind") != "transfer") {
			return bundleError("设备状态与最后签发原件不一致")
		}
	}
	for _, l := range b.Tables["licenses"] {
		id := rowText(l, "id")
		plan := indexes["plans"][rowText(l, "plan_id")]
		product := indexes["products"][rowText(l, "product_id")]
		if rowText(l, "fulfillment_mode") != "offline_file" || indexes["commercial_customers"][rowText(l, "customer_id")] == nil || plan == nil || product == nil || rowText(plan, "product_id") != rowText(l, "product_id") || rowText(product, "license_product_code") != offline.ProductID {
			return bundleError("授权产品与计划关系无效")
		}
		for _, column := range []string{"device_limit", "term_kind", "term_amount", "term_end_date", "term_starts_at"} {
			if _, ok := l[column]; !ok {
				return bundleError("授权缺少期限或设备数量事实")
			}
		}
		deviceLimit := rowInt(l, "device_limit")
		if deviceLimit < 1 {
			return bundleError("授权设备数量无效")
		}
		currentRevision := rowInt(l, "current_revision")
		issuanceID := rowText(l, "current_issuance_id")
		current := indexes["license_issuances"][issuanceID]
		active, bindings := 0, 0
		for _, binding := range b.Tables["offline_device_bindings"] {
			if rowText(binding, "license_id") == id {
				bindings++
				bindingID := rowText(binding, "id")
				latest := latestByBinding[bindingID]
				if string(binding["superseded_at"]) == "null" {
					active++
					if latest.payload.BindingState != "active" || rowInt(latest.row, "revision") != currentRevision {
						return bundleError("当前设备缺少最新签发原件")
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
		record, exists := issuancesByID[issuanceID]
		if current == nil || !exists || highest[id] != currentRevision || int64(len(revisions[id])) != currentRevision || rowInt(current, "revision") != highest[id] || rowText(current, "license_id") != id || rowText(latestByBinding[rowText(current, "binding_id")].row, "id") != issuanceID || !equalBundleTime(l["valid_from"], current["not_before"]) || !equalBundleTime(l["valid_until"], current["expires_at"]) {
			return bundleError("当前授权指针或最高版本不一致")
		}
		currentBinding := indexes["offline_device_bindings"][rowText(current, "binding_id")]
		if (active > 0 && (int64(active) > deviceLimit || string(currentBinding["superseded_at"]) != "null" || record.payload.BindingState != "active")) || (active == 0 && (string(currentBinding["superseded_at"]) == "null" || record.payload.BindingState != "transferred")) {
			return bundleError("当前授权指针未对应当前设备状态")
		}
	}
	for _, binding := range b.Tables["offline_device_bindings"] {
		if indexes["licenses"][rowText(binding, "license_id")] == nil {
			return bundleError("设备缺少所属授权")
		}
	}
	return nil
}

func bundleIssuanceStateValid(kind, state string) bool {
	switch kind {
	case "initial", "activate", "renew":
		return state == "active"
	case "transfer":
		return state == "active" || state == "transferred"
	default:
		return false
	}
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
func bundleLicenseCodesValid(b *BusinessBundle, codes map[string]string) bool {
	ids := make(map[string]bool, len(b.Tables["licenses"]))
	for _, row := range b.Tables["licenses"] {
		id := rowText(row, "id")
		if id == "" || ids[id] {
			return false
		}
		ids[id] = true
	}
	if len(codes) != len(ids) {
		return false
	}
	hashes := make(map[string]bool, len(ids))
	for id, code := range codes {
		if !ids[id] || strings.TrimSpace(code) == "" {
			return false
		}
		hash := license.HashKey(code)
		if hashes[hash] {
			return false
		}
		hashes[hash] = true
	}
	return true
}

func (s *Store) ImportBusinessBundle(ctx context.Context, b *BusinessBundle, licenseCodes map[string]string, actor string, commit bool) (BundleSummary, error) {
	summary := BundleSummary{Customers: len(b.Tables["commercial_customers"]), Licenses: len(b.Tables["licenses"]), Originals: len(b.Tables["license_issuances"]), KeyID: b.PublicIdentity.KeyID, Mappings: []BundleMapping{}}
	if err := ValidateBusinessBundle(b); err != nil {
		return summary, err
	}
	if !bundleLicenseCodesValid(b, licenseCodes) {
		return summary, bundleError("授权码与授权记录不一致")
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
					licenseID := rowText(row, "id")
					l := &model.License{ID: licenseID, LicenseKey: licenseCodes[licenseID]}
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
