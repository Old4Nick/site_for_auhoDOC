// Package migrate applies embedded, versioned SQL without destructive rollback.
package migrate

import (
	"context"
	"crypto/sha256"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var files embed.FS

type migration struct {
	version  string
	sql      string
	checksum string
}

func migrations() ([]migration, error) {
	names, err := fs.Glob(files, "migrations/*.sql")
	if err != nil {
		return nil, err
	}
	sort.Strings(names)
	result := make([]migration, 0, len(names))
	for _, name := range names {
		body, e := files.ReadFile(name)
		if e != nil {
			return nil, e
		}
		result = append(result, migration{version: name[len("migrations/"):], sql: string(body), checksum: fmt.Sprintf("%x", sha256.Sum256(body))})
	}
	return result, nil
}

type queryer interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

func history(ctx context.Context, q queryer) (map[string]string, error) {
	rows, err := q.Query(ctx, `SELECT version,checksum FROM schema_migrations ORDER BY version`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[string]string{}
	for rows.Next() {
		var version, checksum string
		if err = rows.Scan(&version, &checksum); err != nil {
			return nil, err
		}
		result[version] = checksum
	}
	return result, rows.Err()
}

func validateHistory(known []migration, applied map[string]string, requireCurrent bool) error {
	missing := false
	for _, m := range known {
		checksum, ok := applied[m.version]
		if !ok {
			missing = true
			continue
		}
		if missing {
			return fmt.Errorf("migration history is not contiguous; restore the expected schema history")
		}
		if checksum != m.checksum {
			return fmt.Errorf("checksum mismatch for migration %s; restore original migration", m.version)
		}
		delete(applied, m.version)
	}
	if len(applied) > 0 {
		return fmt.Errorf("database schema contains unknown migrations; use the matching application version")
	}
	if requireCurrent && missing {
		return fmt.Errorf("database schema is outdated; run cmd/migrate before starting the server")
	}
	return nil
}

func Apply(ctx context.Context, pool *pgxpool.Pool) error {
	known, err := migrations()
	if err != nil {
		return err
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = tx.Rollback(cleanup)
	}()
	// A transaction-level advisory lock serializes the schema table creation and
	// every migration, including concurrently started first installations.
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(748691835102)`); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
version text PRIMARY KEY, checksum text NOT NULL, applied_at timestamptz NOT NULL DEFAULT clock_timestamp())`); err != nil {
		return err
	}
	applied, err := history(ctx, tx)
	if err != nil {
		return err
	}
	check := make(map[string]string, len(applied))
	for k, v := range applied {
		check[k] = v
	}
	if err = validateHistory(known, check, false); err != nil {
		return err
	}
	for _, m := range known {
		if _, ok := applied[m.version]; ok {
			continue
		}
		if _, err = tx.Exec(ctx, m.sql); err != nil {
			return fmt.Errorf("apply migration %s: %w", m.version, err)
		}
		if _, err = tx.Exec(ctx, `INSERT INTO schema_migrations(version,checksum) VALUES($1,$2)`, m.version, m.checksum); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func CheckSchema(ctx context.Context, pool *pgxpool.Pool) error {
	known, err := migrations()
	if err != nil {
		return err
	}
	var exists bool
	if err = pool.QueryRow(ctx, `SELECT to_regclass('schema_migrations') IS NOT NULL`).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("database schema is missing; run cmd/migrate before starting the server")
	}
	applied, err := history(ctx, pool)
	if err != nil {
		return err
	}
	return validateHistory(known, applied, true)
}
