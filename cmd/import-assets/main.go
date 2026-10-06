package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"equipment-act/internal/config"
	"equipment-act/internal/importer"
	"equipment-act/internal/migrate"
)

func main() { os.Exit(run()) }
func run() int {
	file := flag.String("file", "", "UTF-8 CSV с оборудованием")
	apply := flag.Bool("apply", false, "добавить записи после повторной проверки в транзакции")
	flag.Parse()
	if *file == "" || flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "Укажите --file путь к CSV; --apply включает запись.")
		return 2
	}
	f, e := os.Open(*file)
	if e != nil {
		fmt.Fprintln(os.Stderr, "Не удалось открыть CSV.")
		return 1
	}
	defer f.Close()
	p, e := importer.Parse(f)
	if e != nil {
		fmt.Fprintln(os.Stderr, e.Error())
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	start, cancel := context.WithTimeout(ctx, 20*time.Second)
	pool, e := config.Open(start)
	if e != nil {
		cancel()
		log.Print("PostgreSQL недоступна; проверьте запуск и параметры подключения.")
		return 1
	}
	if e = migrate.CheckSchema(start, pool); e != nil {
		cancel()
		pool.Close()
		log.Print("Схема БД не соответствует приложению; сначала выполните миграции.")
		return 1
	}
	cancel()
	defer pool.Close()
	var report importer.Report
	if *apply {
		report, e = importer.Apply(ctx, pool, p)
	} else {
		report, e = importer.Preview(ctx, pool, p)
	}
	if e != nil {
		log.Print("Проверка или запись CSV не удалась; данные не изменены. Проверьте БД и повторите запуск.")
		return 1
	}
	for _, issue := range report.Issues {
		fmt.Fprintf(os.Stderr, "CSV-запись %d, поле %s: %s\n", issue.Record, issue.Field, issue.Reason)
	}
	mode := "Проверка без записи"
	if report.Applied {
		mode = "Импорт завершён"
	} else if *apply {
		mode = "Импорт отменён: данные не изменены"
	}
	fmt.Printf("%s. Записей: %d; добавить: %d; пропустить: %d; конфликтов: %d; ошибок: %d.\n", mode, report.Total, report.Added, report.Skipped, report.Conflicts, report.Errors)
	if report.Errors > 0 || report.Conflicts > 0 {
		return 2
	}
	return 0
}
