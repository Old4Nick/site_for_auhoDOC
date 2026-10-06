package store

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"equipment-act/internal/assets"
	"equipment-act/internal/money"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrConflict = errors.New("оборудование с таким инвентарным номером уже существует")
	ErrNotFound = errors.New("одно или несколько устройств больше не существуют; обновите список")
)

const QueryTimeout = 5 * time.Second

type Store struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

type SearchResult struct {
	Assets []assets.Asset `json:"assets"`
	More   bool           `json:"more"`
}

const columns = `a.id::text, a.equipment_type, a.model, a.inventory_number,
COALESCE(a.serial_number,''), COALESCE(to_char(a.received_date,'YYYY-MM-DD'),''),
COALESCE(a.current_holder,''), a.price_minor, a.vat_mode, COALESCE(a.vat_rate::text,''),
a.vat_minor, a.updated_at`

type scanner interface{ Scan(...any) error }

func scanAsset(row scanner) (assets.Asset, error) {
	var a assets.Asset
	var updated time.Time
	err := row.Scan(&a.ID, &a.EquipmentType, &a.Model, &a.InventoryNumber, &a.SerialNumber,
		&a.ReceivedDate, &a.CurrentHolder, &a.PriceMinor, &a.VATMode, &a.VATRate, &a.VATMinor, &updated)
	if err != nil {
		return a, err
	}
	if a.VATRate != "" {
		a.VATRate, err = money.ParseRate(a.VATRate)
		if err != nil {
			return a, err
		}
	}
	a.PriceRub = money.Decimal(a.PriceMinor)
	a.VATRub = money.Decimal(a.VATMinor)
	a.CostDisplay = money.Cost(a.PriceMinor, a.VATMinor, a.VATMode, a.VATRate)
	a.Revision = updated.UTC().Format(time.RFC3339Nano)
	return a, nil
}

func (s *Store) Create(ctx context.Context, input assets.Input) (assets.Asset, error) {
	p, err := assets.Validate(input)
	if err != nil {
		return assets.Asset{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, QueryTimeout)
	defer cancel()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return assets.Asset{}, err
	}
	defer rollback(tx)
	a, err := Insert(ctx, tx, p)
	if err != nil {
		return assets.Asset{}, mapError(err)
	}
	if err = tx.Commit(ctx); err != nil {
		return assets.Asset{}, mapError(err)
	}
	return a, nil
}

func rollback(tx pgx.Tx) {
	ctx, cancel := context.WithTimeout(context.Background(), QueryTimeout)
	defer cancel()
	_ = tx.Rollback(ctx)
}

// Insert is also used by the atomic CSV import after validation. A transaction
// owns type reuse and asset creation together. ON CONFLICT waits for concurrent
// type creation and returns the original spelling instead of a second variant.
func Insert(ctx context.Context, tx pgx.Tx, p assets.Prepared) (assets.Asset, error) {
	var canonical string
	err := tx.QueryRow(ctx, `INSERT INTO equipment_types(name) VALUES($1)
ON CONFLICT (lower(btrim(name))) DO UPDATE SET name=equipment_types.name RETURNING name`, p.EquipmentType).Scan(&canonical)
	if err != nil {
		return assets.Asset{}, mapError(err)
	}
	const insert = `INSERT INTO assets AS a (equipment_type,model,inventory_number,serial_number,
received_date,current_holder,price_minor,vat_mode,vat_rate,vat_minor)
VALUES($1,$2,$3,NULLIF($4,''),$5,NULLIF($6,''),$7,$8,NULLIF($9,'')::numeric,$10) RETURNING ` + columns
	a, err := scanAsset(tx.QueryRow(ctx, insert, canonical, p.Model, p.InventoryNumber, p.SerialNumber, p.Received, p.CurrentHolder, p.PriceMinor, p.VATMode, p.VATRate, p.VATMinor))
	return a, mapError(err)
}

func mapError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "assets_inventory_number_normalized_unique" {
		return ErrConflict
	}
	return err
}

// LiteralPattern treats user wildcard and escape characters as ordinary text.
func LiteralPattern(q string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(q)
}

const SearchSQL = `SELECT ` + columns + ` FROM assets a
WHERE ($2='' OR lower(btrim(a.equipment_type))=lower(btrim($2)))
AND ($1='' OR a.equipment_type ILIKE $3 ESCAPE '\' OR a.model ILIKE $3 ESCAPE '\'
OR a.inventory_number ILIKE $3 ESCAPE '\' OR a.serial_number ILIKE $3 ESCAPE '\')
ORDER BY CASE
WHEN lower(btrim(a.inventory_number))=lower(btrim($1)) OR lower(a.serial_number)=lower($1) THEN 0
WHEN a.inventory_number ILIKE $4 ESCAPE '\' OR a.serial_number ILIKE $4 ESCAPE '\'
OR a.equipment_type ILIKE $4 ESCAPE '\' OR a.model ILIKE $4 ESCAPE '\' THEN 1
ELSE 2 END, lower(a.equipment_type), lower(a.model), lower(a.inventory_number), a.id
LIMIT 21`

func (s *Store) Search(ctx context.Context, q, typeFilter string) (SearchResult, error) {
	result := SearchResult{Assets: []assets.Asset{}}
	q = strings.TrimSpace(q)
	typeFilter = strings.TrimSpace(typeFilter)
	if utf8.RuneCountInString(q) > 500 || utf8.RuneCountInString(typeFilter) > 200 || strings.ContainsRune(q, 0) || strings.ContainsRune(typeFilter, 0) {
		return result, &assets.ValidationError{Fields: map[string]string{"q": "поисковая строка слишком длинная или содержит недопустимые символы"}}
	}
	if q == "" && typeFilter == "" {
		return result, nil
	}
	ctx, cancel := context.WithTimeout(ctx, QueryTimeout)
	defer cancel()
	pattern := LiteralPattern(q)
	rows, err := s.pool.Query(ctx, SearchSQL, q, typeFilter, "%"+pattern+"%", pattern+"%")
	if err != nil {
		return result, err
	}
	defer rows.Close()
	for rows.Next() {
		a, e := scanAsset(rows)
		if e != nil {
			return result, e
		}
		result.Assets = append(result.Assets, a)
	}
	if err = rows.Err(); err != nil {
		return result, err
	}
	if len(result.Assets) > 20 {
		result.More = true
		result.Assets = result.Assets[:20]
	}
	return result, nil
}

func (s *Store) Types(ctx context.Context) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, QueryTimeout)
	defer cancel()
	rows, err := s.pool.Query(ctx, `SELECT name FROM equipment_types ORDER BY lower(name),name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	types := []string{}
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			return nil, err
		}
		types = append(types, name)
	}
	return types, rows.Err()
}

func (s *Store) ByIDs(ctx context.Context, ids []string) ([]assets.Asset, error) {
	if len(ids) > 100 {
		return nil, &assets.ValidationError{Fields: map[string]string{"asset_ids": "в одном акте допускается не более 100 устройств"}}
	}
	numbers := make([]int64, len(ids))
	seen := map[int64]bool{}
	for i, id := range ids {
		n, err := strconv.ParseInt(id, 10, 64)
		if err != nil || n <= 0 || strconv.FormatInt(n, 10) != id || seen[n] {
			return nil, &assets.ValidationError{Fields: map[string]string{"asset_ids": "список содержит неверные или повторяющиеся идентификаторы"}}
		}
		numbers[i] = n
		seen[n] = true
	}
	if len(ids) == 0 {
		return []assets.Asset{}, nil
	}
	ctx, cancel := context.WithTimeout(ctx, QueryTimeout)
	defer cancel()
	rows, err := s.pool.Query(ctx, `SELECT `+columns+` FROM unnest($1::bigint[]) WITH ORDINALITY wanted(id,ord)
JOIN assets a ON a.id=wanted.id ORDER BY wanted.ord`, numbers)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]assets.Asset, 0, len(ids))
	for rows.Next() {
		a, e := scanAsset(rows)
		if e != nil {
			return nil, e
		}
		result = append(result, a)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if len(result) != len(ids) {
		return nil, ErrNotFound
	}
	return result, nil
}
