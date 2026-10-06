package importer

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"equipment-act/internal/assets"
	"equipment-act/internal/money"
	"equipment-act/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const MaxBytes = 10 << 20
const MaxRecords = 10000

var Header = []string{"equipment_type", "model", "inventory_number", "serial_number", "received_date", "current_holder", "price_rub", "vat_mode", "vat_rate", "vat_rub"}

type Issue struct {
	Record int    `json:"record"`
	Field  string `json:"field"`
	Reason string `json:"reason"`
}

type Entry struct {
	Record int
	Asset  assets.Prepared
}

type Parsed struct {
	Entries []Entry
	Issues  []Issue
	Total   int
}

type Report struct {
	Total     int
	Added     int
	Skipped   int
	Conflicts int
	Errors    int
	Issues    []Issue
	Applied   bool
}

func addIssue(p *Parsed, record int, field, reason string) {
	p.Issues = append(p.Issues, Issue{Record: record, Field: field, Reason: reason})
}

// Parse reads a bounded UTF-8 CSV with the documented, ordered header.
// Parsing and validation never write to the database.
func Parse(r io.Reader) (Parsed, error) {
	p := Parsed{Entries: []Entry{}, Issues: []Issue{}}
	data, err := io.ReadAll(io.LimitReader(r, MaxBytes+1))
	if err != nil {
		return p, errors.New("не удалось прочитать CSV")
	}
	if len(data) > MaxBytes {
		return p, fmt.Errorf("CSV превышает предел %d МиБ", MaxBytes>>20)
	}
	data = bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf})
	if !utf8.Valid(data) {
		addIssue(&p, 1, "csv", "файл должен быть в UTF-8")
		return p, nil
	}
	cr := csv.NewReader(bytes.NewReader(data))
	cr.Comma = ';'
	cr.FieldsPerRecord = -1
	header, e := cr.Read()
	if e != nil {
		addIssue(&p, 1, "header", "отсутствует корректный заголовок CSV")
		return p, nil
	}
	if len(header) != len(Header) {
		addIssue(&p, 1, "header", "набор или порядок колонок не соответствует контракту")
		return p, nil
	}
	for i, v := range Header {
		if header[i] != v {
			addIssue(&p, 1, "header", "набор или порядок колонок не соответствует контракту")
			return p, nil
		}
	}
	seen := map[string]int{}
	for record := 2; ; record++ {
		row, e := cr.Read()
		if e == io.EOF {
			break
		}
		if e != nil {
			addIssue(&p, record, "csv", "неверная структура CSV-записи")
			break
		}
		p.Total++
		if p.Total > MaxRecords {
			addIssue(&p, record, "csv", "слишком много записей")
			break
		}
		if len(row) != len(Header) {
			addIssue(&p, record, "csv", "неверное число колонок")
			continue
		}
		in := assets.Input{EquipmentType: row[0], Model: row[1], InventoryNumber: row[2], SerialNumber: row[3], ReceivedDate: row[4], CurrentHolder: row[5], PriceRub: row[6], VATMode: row[7], VATRate: row[8], VATRub: row[9]}
		if in.VATMode == "none" && in.VATRub == "" {
			addIssue(&p, record, "vat_rub", "для CSV укажите 0.00")
			continue
		}
		prepared, e := assets.Validate(in)
		if e != nil {
			var v *assets.ValidationError
			if errors.As(e, &v) {
				keys := make([]string, 0, len(v.Fields))
				for k := range v.Fields {
					keys = append(keys, k)
				}
				sort.Strings(keys)
				for _, k := range keys {
					addIssue(&p, record, k, v.Fields[k])
				}
			}
			continue
		}
		key := assets.NormalizeNumber(prepared.InventoryNumber)
		if prev, ok := seen[key]; ok {
			addIssue(&p, record, "inventory_number", fmt.Sprintf("номер повторяется внутри файла (первая запись %d)", prev))
			continue
		}
		seen[key] = record
		p.Entries = append(p.Entries, Entry{Record: record, Asset: prepared})
	}
	return p, nil
}

type queryer interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

const selectExisting = `SELECT inventory_number,equipment_type,model,COALESCE(serial_number,''),
COALESCE(to_char(received_date,'YYYY-MM-DD'),''),COALESCE(current_holder,''),
price_minor,vat_mode,COALESCE(vat_rate::text,''),vat_minor
FROM assets WHERE lower(btrim(inventory_number))=ANY($1::text[])`

func existing(ctx context.Context, q queryer, entries []Entry) (map[string]assets.Input, error) {
	result := make(map[string]assets.Input)
	if len(entries) == 0 {
		return result, nil
	}
	keys := make([]string, 0, len(entries))
	for _, e := range entries {
		keys = append(keys, assets.NormalizeNumber(e.Asset.InventoryNumber))
	}
	rows, err := q.Query(ctx, selectExisting, keys)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var v assets.Input
		var price, vat int64
		if err := rows.Scan(&v.InventoryNumber, &v.EquipmentType, &v.Model, &v.SerialNumber, &v.ReceivedDate, &v.CurrentHolder, &price, &v.VATMode, &v.VATRate, &vat); err != nil {
			return nil, err
		}
		v.PriceRub = money.Decimal(price)
		v.VATRub = money.Decimal(vat)
		if v.VATRate != "" {
			v.VATRate, err = money.ParseRate(v.VATRate)
			if err != nil {
				return nil, err
			}
		}
		result[assets.NormalizeNumber(v.InventoryNumber)] = v
	}
	return result, rows.Err()
}

func same(a, b assets.Input) bool {
	return strings.EqualFold(a.EquipmentType, b.EquipmentType) && a.Model == b.Model &&
		a.InventoryNumber == b.InventoryNumber && a.SerialNumber == b.SerialNumber &&
		a.ReceivedDate == b.ReceivedDate && a.CurrentHolder == b.CurrentHolder &&
		a.PriceRub == b.PriceRub && a.VATMode == b.VATMode && a.VATRate == b.VATRate && a.VATRub == b.VATRub
}

func inspect(ctx context.Context, q queryer, p Parsed) (Report, []Entry, error) {
	report := Report{Total: p.Total, Issues: append([]Issue{}, p.Issues...), Errors: len(p.Issues)}
	current, err := existing(ctx, q, p.Entries)
	if err != nil {
		return report, nil, err
	}
	newEntries := make([]Entry, 0, len(p.Entries))
	for _, e := range p.Entries {
		v, exists := current[assets.NormalizeNumber(e.Asset.InventoryNumber)]
		switch {
		case !exists:
			report.Added++
			newEntries = append(newEntries, e)
		case same(e.Asset.Input, v):
			report.Skipped++
		default:
			report.Conflicts++
			report.Issues = append(report.Issues, Issue{Record: e.Record, Field: "inventory_number", Reason: "номер уже существует с другими реквизитами"})
		}
	}
	return report, newEntries, nil
}

func Preview(ctx context.Context, pool *pgxpool.Pool, p Parsed) (Report, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	r, _, err := inspect(ctx, pool, p)
	return r, err
}

// Apply checks again while holding a write-compatible table lock. Every new
// entry is committed together, or the whole import is rolled back.
func Apply(ctx context.Context, pool *pgxpool.Pool, p Parsed) (Report, error) {
	if len(p.Issues) > 0 {
		return Report{Total: p.Total, Errors: len(p.Issues), Issues: p.Issues}, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	tx, err := pool.Begin(ctx)
	if err != nil {
		return Report{}, err
	}
	defer func() {
		cleanup, c := context.WithTimeout(context.Background(), 5*time.Second)
		defer c()
		_ = tx.Rollback(cleanup)
	}()
	if _, err = tx.Exec(ctx, `LOCK TABLE assets IN SHARE ROW EXCLUSIVE MODE`); err != nil {
		return Report{}, err
	}
	r, newEntries, err := inspect(ctx, tx, p)
	if err != nil {
		return r, err
	}
	if r.Errors > 0 || r.Conflicts > 0 {
		return r, nil
	}
	for _, e := range newEntries {
		if _, err = store.Insert(ctx, tx, e.Asset); err != nil {
			return Report{}, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return Report{}, err
	}
	r.Applied = true
	return r, nil
}
