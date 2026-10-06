package main

import (
	"context"
	"flag"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
	_ "time/tzdata"

	"equipment-act/internal/config"
	"equipment-act/internal/httpapp"
	"equipment-act/internal/migrate"
)

func main() { os.Exit(run()) }
func run() int {
	health := flag.Bool("healthcheck", false, "проверить готовность запущенного сервера")
	flag.Parse()
	addr := config.Value("WEB_ADDR", ":8080")
	_, port, e := net.SplitHostPort(addr)
	if e != nil {
		slog.Error("invalid_WEB_ADDR")
		return 1
	}
	if *health {
		c := http.Client{Timeout: 3 * time.Second}
		r, e := c.Get("http://127.0.0.1:" + port + "/health/ready")
		if e != nil {
			return 1
		}
		defer r.Body.Close()
		if r.StatusCode != 200 {
			return 1
		}
		return 0
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	startup, cancel := context.WithTimeout(ctx, 15*time.Second)
	p, err := config.Open(startup)
	if err != nil {
		cancel()
		slog.Error("PostgreSQL недоступна или неверно настроена; проверьте окружение и запуск db")
		return 1
	}
	defer p.Close()
	if err = migrate.CheckSchema(startup, p); err != nil {
		cancel()
		slog.Error("Схема БД несовместима; выполните cmd/migrate и проверьте версии и контрольные суммы миграций")
		return 1
	}
	cancel()
	origins := []string{"http://localhost:" + port, "http://127.0.0.1:" + port}
	if v := os.Getenv("APP_ORIGIN"); v != "" {
		origins = append(origins, strings.TrimRight(v, "/"))
	}
	h, err := httpapp.New(p, origins)
	if err != nil {
		slog.Error("Не удалось загрузить интерфейс или APP_ORIGIN")
		return 1
	}
	s := http.Server{Addr: addr, Handler: h, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10, ErrorLog: slog.NewLogLogger(slog.Default().Handler(), slog.LevelError)}
	end := make(chan error, 1)
	go func() { end <- s.ListenAndServe() }()
	slog.Info("web_started", "port", port)
	select {
	case err = <-end:
		if err != http.ErrServerClosed {
			slog.Error("http_listen_failed")
			return 1
		}
	case <-ctx.Done():
		shutdown, c := context.WithTimeout(context.Background(), 10*time.Second)
		defer c()
		if e := s.Shutdown(shutdown); e != nil {
			_ = s.Close()
			slog.Error("http_shutdown_timeout")
			return 1
		}
	}
	return 0
}
