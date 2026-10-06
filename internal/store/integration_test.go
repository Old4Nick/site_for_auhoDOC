package store

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"equipment-act/internal/assets"
	"equipment-act/internal/migrate"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// The opt-in test creates and removes its own random schema only. Use a
// disposable PostgreSQL database whose account can install pg_trgm.
func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL is not set; PostgreSQL integration not run")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	base, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal("cannot configure integration PostgreSQL")
	}
	if err = base.Ping(ctx); err != nil {
		base.Close()
		t.Fatal("integration PostgreSQL is not reachable")
	}
	var suffix [8]byte
	if _, err = rand.Read(suffix[:]); err != nil {
		base.Close()
		t.Fatal(err)
	}
	schema := fmt.Sprintf("test_assets_%x", suffix)
	if _, err = base.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{schema}.Sanitize()); err != nil {
		base.Close()
		t.Fatal("cannot create isolated test schema")
	}
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal("cannot configure integration pool")
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal("cannot open isolated integration pool")
	}
	t.Cleanup(func() {
		pool.Close()
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cleanupCancel()
		if _, e := base.Exec(cleanupCtx, "DROP SCHEMA "+pgx.Identifier{schema}.Sanitize()+" CASCADE"); e != nil {
			t.Error("could not remove isolated test schema")
		}
		base.Close()
	})
	if err = migrate.Apply(ctx, pool); err != nil {
		t.Fatalf("migrations: %v", err)
	}
	if err = migrate.Apply(ctx, pool); err != nil {
		t.Fatalf("repeat migrations: %v", err)
	}
	if err = migrate.CheckSchema(ctx, pool); err != nil {
		t.Fatalf("schema check: %v", err)
	}
	return pool
}

func demoInput(number, serial string) assets.Input {
	return assets.Input{EquipmentType: "Ноутбук", Model: "Одинаковая модель", InventoryNumber: number, SerialNumber: serial, CurrentHolder: "Склад", PriceRub: "27600.00", VATMode: "included", VATRate: "22", VATRub: "4977.05"}
}

func TestPostgreSQLWorkflow(t *testing.T) {
	pool := testPool(t)
	s := New(pool)
	ctx := context.Background()
	a, err := s.Create(ctx, demoInput("  DEMO-001\u00a0", "КИРИЛЛ-A%_\\001"))
	if err != nil {
		t.Fatal(err)
	}
	in := demoInput("DEMO-002", "КИРИЛЛ-002")
	in.EquipmentType = "ноутбук"
	b, err := s.Create(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if b.EquipmentType != "Ноутбук" {
		t.Fatal("type spelling was not reused")
	}
	if a.InventoryNumber != "DEMO-001" || a.ReceivedDate != "" || a.VATRub != "4977.05" {
		t.Fatalf("wrong stored facts: %+v", a)
	}
	_, err = s.Create(ctx, demoInput("\t demo-001\u3000", "other"))
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("normalized uniqueness not enforced: %v", err)
	}
	for _, q := range []string{"рилл-a", "%_\\", "a%_\\001"} {
		result, e := s.Search(ctx, q, "")
		if e != nil || len(result.Assets) != 1 || result.Assets[0].ID != a.ID {
			t.Fatalf("literal substring %q: %+v %v", q, result, e)
		}
	}
	empty, err := s.Search(ctx, "", "")
	if err != nil || len(empty.Assets) != 0 || empty.More {
		t.Fatal("empty search returned inventory")
	}
	typed, err := s.Search(ctx, "", "НОУТБУК")
	if err != nil || len(typed.Assets) != 2 {
		t.Fatalf("short type filter: %+v %v", typed, err)
	}
	ordered, err := s.ByIDs(ctx, []string{b.ID, a.ID})
	if err != nil || len(ordered) != 2 || ordered[0].ID != b.ID || ordered[1].ID != a.ID {
		t.Fatalf("selection order lost: %+v %v", ordered, err)
	}
	if _, err = s.ByIDs(ctx, []string{a.ID, a.ID}); err == nil {
		t.Fatal("duplicate act item accepted")
	}
	if _, err = s.ByIDs(ctx, []string{"9223372036854775807"}); !errors.Is(err, ErrNotFound) {
		t.Fatal("missing item accepted")
	}
	if _, err = pool.Exec(ctx, `UPDATE assets SET current_holder='Другой склад', price_minor=2800000 WHERE id=$1`, a.ID); err != nil {
		t.Fatal(err)
	}
	fresh, err := s.ByIDs(ctx, []string{a.ID})
	if err != nil || fresh[0].Revision == a.Revision || fresh[0].PriceRub != "28000.00" || fresh[0].CurrentHolder != "Другой склад" {
		t.Fatal("selection did not reread current database values", err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err = s.Search(cancelled, "DEMO", ""); err == nil {
		t.Fatal("cancelled SQL request succeeded")
	}
	t.Run("constraints", func(t *testing.T) {
		for _, query := range []string{
			`UPDATE assets SET vat_minor=price_minor+1 WHERE id=$1`,
			`UPDATE assets SET vat_mode='none' WHERE id=$1`,
			`UPDATE assets SET vat_rate=NULL WHERE id=$1`,
			`UPDATE assets SET price_minor=-1 WHERE id=$1`,
			`UPDATE assets SET inventory_number=U&'\00A0' || inventory_number WHERE id=$1`,
		} {
			if _, e := pool.Exec(ctx, query, a.ID); e == nil {
				t.Errorf("database accepted invalid values: %s", query)
			}
		}
	})
	t.Run("limit and exact priority", func(t *testing.T) {
		for i := 0; i < 24; i++ {
			in := demoInput(fmt.Sprintf("MATCH-%02d", i), fmt.Sprintf("DEMO-MATCH-%02d", i))
			if _, e := s.Create(ctx, in); e != nil {
				t.Fatal(e)
			}
		}
		exact, e := s.Create(ctx, demoInput("MATCH", "EXACT-SERIAL"))
		if e != nil {
			t.Fatal(e)
		}
		result, e := s.Search(ctx, "MATCH", "")
		if e != nil || len(result.Assets) != 20 || !result.More || result.Assets[0].ID != exact.ID {
			t.Fatalf("bounded ranked search: %+v %v", result, e)
		}
		repeated, e := s.Search(ctx, "MATCH", "")
		if e != nil {
			t.Fatal(e)
		}
		for i, a := range result.Assets {
			if repeated.Assets[i].ID != a.ID {
				t.Fatal("unstable search ordering")
			}
		}
	})
	t.Run("concurrent type reuse", func(t *testing.T) {
		var wg sync.WaitGroup
		results := make(chan assets.Asset, 2)
		failures := make(chan error, 2)
		for i, name := range []string{"ПрИнТеР", "принтер"} {
			wg.Add(1)
			go func(i int, name string) {
				defer wg.Done()
				in := demoInput(fmt.Sprintf("PRINTER-%d", i), "")
				in.EquipmentType = name
				a, e := s.Create(ctx, in)
				if e != nil {
					failures <- e
				} else {
					results <- a
				}
			}(i, name)
		}
		wg.Wait()
		close(results)
		close(failures)
		for e := range failures {
			t.Fatal(e)
		}
		canonical := ""
		count := 0
		for a := range results {
			if canonical != "" && a.EquipmentType != canonical {
				t.Fatal("concurrent type created variants")
			}
			canonical = a.EquipmentType
			count++
		}
		if count != 2 {
			t.Fatal("concurrent create missing result")
		}
	})
	t.Run("migration checksum prevents startup", func(t *testing.T) {
		if _, e := pool.Exec(ctx, `UPDATE schema_migrations SET checksum='changed' WHERE version='002_search.sql'`); e != nil {
			t.Fatal(e)
		}
		if e := migrate.CheckSchema(ctx, pool); e == nil {
			t.Fatal("changed migration accepted at startup")
		}
		if e := migrate.Apply(ctx, pool); e == nil {
			t.Fatal("changed applied migration accepted")
		}
	})
}
