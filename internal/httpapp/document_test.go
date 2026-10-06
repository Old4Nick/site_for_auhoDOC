package httpapp

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"equipment-act/internal/assets"
	"equipment-act/internal/migrate"
	"equipment-act/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestDocumentPostgreSQL(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	base, e := pgxpool.New(ctx, dsn)
	if e != nil {
		t.Fatal(e)
	}
	defer base.Close()
	schema := "test_document_" + token()[:12]
	if _, e = base.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{schema}.Sanitize()); e != nil {
		t.Fatal(e)
	}
	defer func() {
		_, e := base.Exec(ctx, "DROP SCHEMA "+pgx.Identifier{schema}.Sanitize()+" CASCADE")
		if e != nil {
			t.Error(e)
		}
	}()
	cfg, e := pgxpool.ParseConfig(dsn)
	if e != nil {
		t.Fatal(e)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	pool, e := pgxpool.NewWithConfig(ctx, cfg)
	if e != nil {
		t.Fatal(e)
	}
	defer pool.Close()
	if e = migrate.Apply(ctx, pool); e != nil {
		t.Fatal(e)
	}
	s := store.New(pool)
	a, e := s.Create(ctx, assets.Input{EquipmentType: "Ноутбук", Model: "Исходная модель", InventoryNumber: "TEST-C-1", CurrentHolder: "Склад", PriceRub: "27600.00", VATMode: "included", VATRate: "20", VATRub: "4600.00"})
	if e != nil {
		t.Fatal(e)
	}
	b, e := s.Create(ctx, assets.Input{EquipmentType: "Монитор", Model: "Тест & <Б>", InventoryNumber: "TEST-C-2", PriceRub: "2409.00", VATMode: "none", VATRub: "0.00"})
	if e != nil {
		t.Fatal(e)
	}
	handler, e := New(pool, []string{"http://localhost"})
	if e != nil {
		t.Fatal(e)
	}
	post := func(body any, csrf bool) *httptest.ResponseRecorder {
		data, e := json.Marshal(body)
		if e != nil {
			t.Fatal(e)
		}
		r := httptest.NewRequest("POST", "http://localhost/api/act/document", bytes.NewReader(data))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", "http://localhost")
		if csrf {
			v := strings.Repeat("a", 64)
			r.Header.Set("X-CSRF-Token", v)
			r.AddCookie(&http.Cookie{Name: "act_csrf", Value: v})
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	in := documentRequest{IDs: []string{b.ID, a.ID}, Revisions: map[string]string{a.ID: a.Revision, b.ID: b.Revision}, Number: "ПРОВЕРКА-1", Date: "2026-10-05", Recipient: "Иванов & <Тест>"}
	if w := post(in, false); w.Code != 403 {
		t.Fatalf("csrf %d", w.Code)
	}
	checkFile := func(w *httptest.ResponseRecorder, model string) {
		t.Helper()
		if w.Code != 200 {
			t.Fatalf("status %d: %s", w.Code, w.Body.String())
		}
		if !strings.Contains(w.Header().Get("Content-Disposition"), "attachment") || !strings.Contains(w.Header().Get("Content-Type"), "wordprocessingml") {
			t.Fatal("wrong download headers")
		}
		z, e := zip.NewReader(bytes.NewReader(w.Body.Bytes()), int64(w.Body.Len()))
		if e != nil {
			t.Fatal(e)
		}
		var text string
		for _, f := range z.File {
			if f.Name == "word/document.xml" {
				r, e := f.Open()
				if e != nil {
					t.Fatal(e)
				}
				data, e := io.ReadAll(r)
				r.Close()
				if e != nil {
					t.Fatal(e)
				}
				text = string(data)
			}
		}
		for _, want := range []string{model, "30 009 руб. 00 коп.", "Иванов &amp; &lt;Тест&gt;"} {
			if !strings.Contains(text, want) {
				t.Fatalf("missing %s", want)
			}
		}
		if strings.Index(text, "TEST-C-2") > strings.Index(text, "TEST-C-1") {
			t.Fatal("order lost")
		}
	}
	checkFile(post(in, true), "Исходная модель")
	blank := in
	blank.Number = ""
	if w := post(blank, true); w.Code != 422 {
		t.Fatal("missing act number accepted")
	}
	badDate := in
	badDate.Date = "2026-02-30"
	if w := post(badDate, true); w.Code != 422 {
		t.Fatal("invalid date accepted")
	}
	dup := in
	dup.IDs = []string{a.ID, a.ID}
	if w := post(dup, true); w.Code != 422 {
		t.Fatal("duplicate accepted")
	}
	missing := in
	missing.IDs = []string{"9999999"}
	missing.Revisions = map[string]string{"9999999": "stale"}
	if w := post(missing, true); w.Code != 409 {
		t.Fatal("missing accepted")
	}
	if w := post(map[string]any{"ids": in.IDs, "price_rub": "1.00"}, true); w.Code != 400 {
		t.Fatal("client price accepted")
	}
	if _, e := pool.Exec(ctx, "UPDATE assets SET model='Свежая модель',updated_at=clock_timestamp() WHERE id=$1", a.ID); e != nil {
		t.Fatal(e)
	}
	stale := post(in, true)
	if stale.Code != 409 || strings.Contains(stale.Header().Get("Content-Type"), "wordprocessingml") {
		t.Fatal("stale data became a file")
	}
	fresh, e := s.ByIDs(ctx, []string{a.ID})
	if e != nil {
		t.Fatal(e)
	}
	in.Revisions[a.ID] = fresh[0].Revision
	checkFile(post(in, true), "Свежая модель")
	after, e := s.ByIDs(ctx, []string{a.ID})
	if e != nil {
		t.Fatal(e)
	}
	if after[0].CurrentHolder != "Склад" || after[0].Revision != fresh[0].Revision {
		t.Fatal("generation mutated asset")
	}
	// Even corrupted imported text must produce JSON, never a partially sent ZIP.
	if _, e := pool.Exec(ctx, "UPDATE assets SET model=$1 WHERE id=$2", "bad\x01text", a.ID); e != nil {
		t.Fatal(e)
	}
	corrupted, e := s.ByIDs(ctx, []string{a.ID})
	if e != nil {
		t.Fatal(e)
	}
	in.Revisions[a.ID] = corrupted[0].Revision
	failed := post(in, true)
	if failed.Code != 500 || !strings.Contains(failed.Header().Get("Content-Type"), "application/json") || bytes.HasPrefix(failed.Body.Bytes(), []byte("PK")) {
		t.Fatal("generation error returned a document")
	}
}
