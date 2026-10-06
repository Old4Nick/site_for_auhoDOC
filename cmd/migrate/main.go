package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"equipment-act/internal/config"
	"equipment-act/internal/migrate"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	pool, err := config.Open(ctx)
	if err != nil {
		log.Fatal("Database connection failed. Check the database service and environment settings.")
	}
	defer pool.Close()
	if err = migrate.Apply(ctx, pool); err != nil {
		log.Fatal("Migration failed. Check database permissions and that embedded migration files match the applied schema. No partial migration was committed.")
	}
	log.Print("Database migrations applied successfully.")
}
