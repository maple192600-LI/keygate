package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"github.com/uptrace/bun"
	"reflect"
	"sort"
	"strings"
)

// A logical snapshot contains data only. Restoring never executes SQL from an
// archive: schema, functions and identifiers come from installed migrations.
type LocalSnapshot struct {
	Tables    map[string][]BundleRow   `json:"tables"`
	Sequences map[string]SequenceValue `json:"sequences"`
}
type SequenceValue struct {
	Value  int64 `json:"value"`
	Called bool  `json:"called"`
}

func quoteIdentifier(v string) string { return `"` + strings.ReplaceAll(v, `"`, `""`) + `"` }
func snapshotTables(ctx context.Context, db bun.IDB) ([]string, error) {
	var names []string
	err := db.NewRaw("SELECT table_name FROM information_schema.tables WHERE table_schema='public' AND table_type='BASE TABLE' ORDER BY table_name").Scan(ctx, &names)
	return names, err
}
func SnapshotIn(ctx context.Context, db bun.IDB) (*LocalSnapshot, error) {
	result := &LocalSnapshot{Tables: map[string][]BundleRow{}, Sequences: map[string]SequenceValue{}}
	names, err := snapshotTables(ctx, db)
	if err != nil {
		return nil, err
	}
	for _, name := range names {
		rows, err := readBundleRows(ctx, db, quoteIdentifier(name), "*", "", "row_to_json("+quoteIdentifier(name)+")::text")
		if err != nil {
			return nil, err
		}
		result.Tables[name] = rows
	}
	var seqs []string
	if err := db.NewRaw("SELECT sequencename FROM pg_sequences WHERE schemaname='public' ORDER BY sequencename").Scan(ctx, &seqs); err != nil {
		return nil, err
	}
	for _, name := range seqs {
		var v SequenceValue
		if err := db.NewRaw("SELECT last_value AS value,is_called AS called FROM "+quoteIdentifier(name)).Scan(ctx, &v); err != nil {
			return nil, err
		}
		result.Sequences[name] = v
	}
	return result, nil
}
func (s *Store) Snapshot(ctx context.Context) (*LocalSnapshot, error) {
	var result *LocalSnapshot
	err := s.DB.RunInTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true}, func(ctx context.Context, tx bun.Tx) error {
		var err error
		result, err = SnapshotIn(ctx, tx)
		return err
	})
	return result, err
}
func migrationSet(rows []BundleRow) map[string]string {
	r := map[string]string{}
	for _, v := range rows {
		r[rowText(v, "filename")] = rowText(v, "checksum")
	}
	return r
}
func canonicalRows(rows []BundleRow) []string {
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		raw, _ := json.Marshal(row)
		var value any
		d := json.NewDecoder(strings.NewReader(string(raw)))
		d.UseNumber()
		_ = d.Decode(&value)
		raw, _ = json.Marshal(value)
		out = append(out, string(raw))
	}
	sort.Strings(out)
	return out
}
func (s *Store) RestoreSnapshot(ctx context.Context, snapshot *LocalSnapshot) error {
	current, err := s.Snapshot(ctx)
	if err != nil {
		return err
	}
	if len(current.Tables["schema_migrations"]) != len(snapshot.Tables["schema_migrations"]) || !reflect.DeepEqual(migrationSet(current.Tables["schema_migrations"]), migrationSet(snapshot.Tables["schema_migrations"])) || len(current.Tables) != len(snapshot.Tables) || len(current.Sequences) != len(snapshot.Sequences) {
		return bundleError("备份数据库版本与当前程序不一致")
	}
	for name := range current.Tables {
		if snapshot.Tables[name] == nil {
			return bundleError("备份缺少数据库表")
		}
		if name != "schema_migrations" && len(current.Tables[name]) != 0 {
			return bundleError("恢复候选数据库不是空库")
		}
	}
	for name := range current.Sequences {
		if v, ok := snapshot.Sequences[name]; !ok || v.Value < 1 {
			return bundleError("备份序列无效")
		}
	}
	type foreignKey struct {
		Table     string
		Name      string
		Deferred  bool
		Initially bool
	}
	var constraints []foreignKey
	if err := s.DB.NewRaw(`SELECT t.relname AS "table",c.conname AS name,c.condeferrable AS deferred,c.condeferred AS initially FROM pg_constraint c JOIN pg_namespace n ON n.oid=c.connamespace JOIN pg_class t ON t.oid=c.conrelid WHERE n.nspname='public' AND c.contype='f' ORDER BY c.conname`).Scan(ctx, &constraints); err != nil {
		return err
	}
	err = s.DB.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		if err := AllowOfflineWriteIn(ctx, tx, "restore"); err != nil {
			return err
		}
		// Deferral admits mutually referring rows, then validates every original FK
		// before commit. No trigger is disabled and no history row is rewritten.
		for _, c := range constraints {
			if _, err := tx.ExecContext(ctx, "ALTER TABLE "+quoteIdentifier(c.Table)+" ALTER CONSTRAINT "+quoteIdentifier(c.Name)+" DEFERRABLE INITIALLY DEFERRED"); err != nil {
				return err
			}
		}
		// Parent rows must exist for the installed business guard triggers,
		// even though ordinary foreign keys are checked at transaction end.
		priority := []string{"settings", "users", "products", "plans", "commercial_customers", "offline_requests", "licenses"}
		if _, ok := current.Tables["offline_actors"]; ok {
			priority = append([]string{"offline_actors"}, priority...)
		}
		names := append([]string{}, priority...)
		first := map[string]bool{}
		for _, name := range priority {
			first[name] = true
		}
		for name := range current.Tables {
			if !first[name] {
				names = append(names, name)
			}
		}
		for _, name := range names {
			if name == "schema_migrations" {
				continue
			}
			columns, err := bundleColumns(ctx, tx, name)
			if err != nil {
				return err
			}
			for _, row := range snapshot.Tables[name] {
				if err := insertBundleRow(ctx, tx, name, row, columns); err != nil {
					return fmt.Errorf("restore table %s: %w", name, err)
				}
			}
		}
		if _, err := tx.ExecContext(ctx, "SET CONSTRAINTS ALL IMMEDIATE"); err != nil {
			return err
		}
		for _, c := range constraints {
			mode := "NOT DEFERRABLE"
			if c.Deferred {
				mode = "DEFERRABLE INITIALLY IMMEDIATE"
				if c.Initially {
					mode = "DEFERRABLE INITIALLY DEFERRED"
				}
			}
			if _, err := tx.ExecContext(ctx, "ALTER TABLE "+quoteIdentifier(c.Table)+" ALTER CONSTRAINT "+quoteIdentifier(c.Name)+" "+mode); err != nil {
				return err
			}
		}
		// Inserting plans legitimately appends current update terms. In this
		// NEW candidate only, remove those generated IDs; restore preserves the
		// exact historical IDs and timestamps from the consistent snapshot.
		termIDs := []string{}
		for _, row := range snapshot.Tables["plan_update_terms"] {
			termIDs = append(termIDs, rowText(row, "id"))
		}
		idsJSON, _ := json.Marshal(termIDs)
		if _, err := tx.ExecContext(ctx, `DELETE FROM plan_update_terms WHERE id NOT IN (SELECT json_array_elements_text(?::json))`, string(idsJSON)); err != nil {
			return err
		}
		actual, err := SnapshotIn(ctx, tx)
		if err != nil {
			return err
		}
		for name, rows := range snapshot.Tables {
			if name == "schema_migrations" {
				continue
			}
			if !reflect.DeepEqual(canonicalRows(rows), canonicalRows(actual.Tables[name])) {
				return bundleError("恢复回读与备份不一致：" + name)
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	for name, v := range snapshot.Sequences {
		if _, err := s.DB.ExecContext(ctx, "SELECT setval(?::regclass,?,?)", name, v.Value, v.Called); err != nil {
			return err
		}
	}
	return nil
}
