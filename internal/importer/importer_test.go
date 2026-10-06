package importer

import (
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"equipment-act/internal/assets"
	"equipment-act/internal/migrate"
	"equipment-act/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const header = "equipment_type;model;inventory_number;serial_number;received_date;current_holder;price_rub;vat_mode;vat_rate;vat_rub\n"
const first = "Ноутбук;Модель А;IMPORT-001;SN-001;2026-09-01;Склад;27600.00;included;22;4977.05\n"
const second = "Монитор;Модель Б;IMPORT-002;;2026-09-02;;2409.00;none;;0.00\n"

func parsed(t *testing.T, body string) Parsed {
	t.Helper()
	p, err := Parse(strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestCSVContract(t *testing.T) {
	p := parsed(t, "\ufeff"+header+first+second)
	if p.Total != 2 || len(p.Entries) != 2 || len(p.Issues) != 0 || p.Entries[0].Asset.PriceMinor != 2760000 {
		t.Fatalf("valid CSV rejected: %+v", p)
	}
	bad := parsed(t, header+first+"Монитор;Ошибка;IMPORT-003;;bad;;12.5;none;;0.00\n"+"Монитор;Другая модель; import-001 ;;;;1.00;none;;0.00\n")
	if bad.Total != 3 || len(bad.Issues) < 2 {
		t.Fatalf("invalid rows accepted: %+v", bad)
	}
	duplicate := parsed(t, header+first+"Ноутбук;Модель А; import-001 ;SN-002;;;1.00;none;;0.00\n")
	if len(duplicate.Issues) != 1 || duplicate.Issues[0].Field != "inventory_number" || duplicate.Issues[0].Record != 3 {
		t.Fatalf("duplicate not reported: %+v", duplicate.Issues)
	}
	wrongHeader := parsed(t, strings.Replace(header, "inventory_number", "number", 1)+first)
	if len(wrongHeader.Issues) != 1 || wrongHeader.Issues[0].Field != "header" {
		t.Fatal("header mismatch not reported")
	}
	oversize := strings.Repeat("A", MaxBytes+1)
	if _, err := Parse(strings.NewReader(oversize)); err == nil {
		t.Fatal("size limit not enforced")
	}
}

func integrationPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	u := os.Getenv("TEST_DATABASE_URL")
	if u == "" {
		t.Skip("TEST_DATABASE_URL unset; PostgreSQL integration not run")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	base, err := pgxpool.New(ctx, u)
	if err != nil {
		t.Fatal("database config")
	}
	if err = base.Ping(ctx); err != nil {
		base.Close()
		t.Fatal("database unavailable")
	}
	var bytes [8]byte
	if _, err = rand.Read(bytes[:]); err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("test_import_%x", bytes)
	if _, err = base.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{schema}.Sanitize()); err != nil {
		base.Close()
		t.Fatal("create schema")
	}
	cfg, err := pgxpool.ParseConfig(u)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	p, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		p.Close()
		clean, c := context.WithTimeout(context.Background(), 20*time.Second)
		defer c()
		if _, e := base.Exec(clean, "DROP SCHEMA "+pgx.Identifier{schema}.Sanitize()+" CASCADE"); e != nil {
			t.Error("could not remove test schema")
		}
		base.Close()
	})
	if err = migrate.Apply(ctx, p); err != nil {
		t.Fatalf("migrations: %v", err)
	}
	return p
}

func count(t *testing.T, p *pgxpool.Pool) int {
	t.Helper()
	var n int
	if err := p.QueryRow(context.Background(), "SELECT count(*) FROM assets").Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestImportAtomicityAndIdempotency(t *testing.T) {
	p := integrationPool(t)
	ctx := context.Background()
	file := parsed(t, header+first+second)
	preview, err := Preview(ctx, p, file)
	if err != nil {
		t.Fatal(err)
	}
	if preview.Added != 2 || preview.Applied || count(t, p) != 0 {
		t.Fatalf("preview wrote data: %+v", preview)
	}
	r, err := Apply(ctx, p, file)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Applied || r.Added != 2 || count(t, p) != 2 {
		t.Fatalf("first import: %+v", r)
	}
	r, err = Apply(ctx, p, file)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Applied || r.Skipped != 2 || r.Added != 0 || count(t, p) != 2 {
		t.Fatalf("repeat import: %+v", r)
	}
	conflict := parsed(t, header+strings.Replace(first, "Модель А", "Другие реквизиты", 1)+"Монитор;Третья;IMPORT-003;;;;1.00;none;;0.00\n")
	r, err = Apply(ctx, p, conflict)
	if err != nil {
		t.Fatal(err)
	}
	if r.Applied || r.Conflicts != 1 || count(t, p) != 2 {
		t.Fatalf("conflict allowed partial insert: %+v", r)
	}
	badMiddle := parsed(t, header+"Монитор;Третья;IMPORT-003;;;;1.00;none;;0.00\n"+"Монитор;Плохая;IMPORT-004;;;;oops;none;;0.00\n"+"Монитор;Пятая;IMPORT-005;;;;1.00;none;;0.00\n")
	r, err = Apply(ctx, p, badMiddle)
	if err != nil {
		t.Fatal(err)
	}
	if r.Applied || r.Errors == 0 || count(t, p) != 2 {
		t.Fatalf("invalid middle row allowed partial insert: %+v", r)
	}
	// A changed database between preview and apply must be checked again.
	stale := parsed(t, header+"Ноутбук;Новая;IMPORT-006;;;;1.00;none;;0.00\n")
	if r, err = Preview(ctx, p, stale); err != nil || r.Added != 1 {
		t.Fatalf("preview failed: %+v %v", r, err)
	}
	if _, err = store.New(p).Create(ctx, assets.Input{EquipmentType: "Ноутбук", Model: "Иная модель", InventoryNumber: "IMPORT-006", PriceRub: "2.00", VATMode: "none", VATRub: "0.00"}); err != nil {
		t.Fatal(err)
	}
	r, err = Apply(ctx, p, stale)
	if err != nil {
		t.Fatal(err)
	}
	if r.Applied || r.Conflicts != 1 || count(t, p) != 3 {
		t.Fatalf("stale preview was trusted: %+v", r)
	}
}
