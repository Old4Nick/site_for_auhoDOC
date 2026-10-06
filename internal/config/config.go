package config

import (
	"context"
	"errors"
	"os"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func Value(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

func PoolConfig() (*pgxpool.Config, error) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		dsn = "postgres://localhost/equipment?sslmode=disable"
	}
	c, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, errors.New("неверные параметры подключения к PostgreSQL")
	}
	if os.Getenv("DATABASE_URL") == "" {
		port, e := strconv.ParseUint(Value("DB_PORT", "5432"), 10, 16)
		if e != nil || port == 0 {
			return nil, errors.New("DB_PORT должен быть номером порта")
		}
		c.ConnConfig.Host = Value("DB_HOST", "localhost")
		c.ConnConfig.Port = uint16(port)
		c.ConnConfig.Database = Value("DB_NAME", "equipment")
		c.ConnConfig.User = Value("DB_USER", "equipment")
		c.ConnConfig.Password = os.Getenv("DB_PASSWORD")
	}
	c.MaxConns = 8
	c.MinConns = 0
	c.MaxConnIdleTime = 5 * time.Minute
	c.ConnConfig.ConnectTimeout = 5 * time.Second
	c.ConnConfig.RuntimeParams["statement_timeout"] = "5000"
	return c, nil
}

func Open(ctx context.Context) (*pgxpool.Pool, error) {
	c, err := PoolConfig()
	if err != nil {
		return nil, err
	}
	p, err := pgxpool.NewWithConfig(ctx, c)
	if err != nil {
		return nil, errors.New("не удалось создать подключение PostgreSQL")
	}
	if err = p.Ping(ctx); err != nil {
		p.Close()
		return nil, errors.New("PostgreSQL недоступна: проверьте запуск БД и параметры подключения")
	}
	return p, nil
}
