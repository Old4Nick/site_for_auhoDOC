package httpapp

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"html/template"
	"io"
	"io/fs"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"equipment-act/internal/assets"
	"equipment-act/internal/docgen"
	"equipment-act/internal/money"
	"equipment-act/internal/store"
	"equipment-act/web"
	"github.com/jackc/pgx/v5/pgxpool"
)

type App struct {
	pool    *pgxpool.Pool
	store   *store.Store
	page    *template.Template
	origins map[string]bool
	hosts   map[string]bool
	moscow  *time.Location
}

func New(pool *pgxpool.Pool, origins []string) (http.Handler, error) {
	p, err := template.ParseFS(web.FS, "templates/index.html")
	if err != nil {
		return nil, err
	}
	tz, err := time.LoadLocation("Europe/Moscow")
	if err != nil {
		return nil, err
	}
	a := &App{pool: pool, store: store.New(pool), page: p, origins: map[string]bool{}, hosts: map[string]bool{}, moscow: tz}
	for _, s := range origins {
		u, e := url.Parse(s)
		if e != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
			return nil, errors.New("APP_ORIGIN должен содержать только http(s)://имя:порт")
		}
		a.origins[s] = true
		a.hosts[u.Host] = true
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", a.index)
	static, err := fs.Sub(web.FS, "static")
	if err != nil {
		return nil, err
	}
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(static))))
	mux.HandleFunc("GET /health/ready", a.ready)
	mux.HandleFunc("GET /api/types", a.types)
	mux.HandleFunc("GET /api/assets", a.search)
	mux.Handle("POST /api/assets", a.csrf(http.HandlerFunc(a.create)))
	mux.Handle("POST /api/act/preview", a.csrf(http.HandlerFunc(a.preview)))
	mux.Handle("POST /api/act/document", a.csrf(http.HandlerFunc(a.document)))
	return a.guard(mux), nil
}

func (a *App) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
		w.Header().Set("Cache-Control", "no-store")
		if !a.hosts[r.Host] {
			fail(w, 400, "Недопустимый адрес приложения.", nil)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *App) csrf(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		c, err := r.Cookie("act_csrf")
		t := r.Header.Get("X-CSRF-Token")
		if !a.origins[origin] || r.Header.Get("Sec-Fetch-Site") == "cross-site" || err != nil || len(t) != 64 || len(c.Value) != 64 || subtle.ConstantTimeCompare([]byte(t), []byte(c.Value)) != 1 {
			fail(w, 403, "Проверка безопасности не пройдена. Обновите страницу и повторите действие.", nil)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func token() string {
	var b [32]byte
	if _, e := rand.Read(b[:]); e != nil {
		panic("random unavailable")
	}
	return hex.EncodeToString(b[:])
}

func (a *App) index(w http.ResponseWriter, r *http.Request) {
	t := token()
	if c, e := r.Cookie("act_csrf"); e == nil && len(c.Value) == 64 {
		if _, e = hex.DecodeString(c.Value); e == nil {
			t = c.Value
		}
	}
	http.SetCookie(w, &http.Cookie{Name: "act_csrf", Value: t, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: r.TLS != nil, MaxAge: 28800})
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := a.page.ExecuteTemplate(w, "index.html", struct{ CSRFToken, Today string }{t, time.Now().In(a.moscow).Format("2006-01-02")}); err != nil {
		slog.Error("template_render_failed", "error_id", token()[:12])
	}
}

func (a *App) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := a.pool.Ping(ctx); err != nil {
		fail(w, 503, "База данных недоступна.", nil)
		return
	}
	respond(w, 200, map[string]string{"status": "ready"})
}

func (a *App) types(w http.ResponseWriter, r *http.Request) {
	t, err := a.store.Types(r.Context())
	if err != nil {
		a.dbError(w, err)
		return
	}
	if t == nil {
		t = []string{}
	}
	respond(w, 200, map[string]any{"types": t})
}

func (a *App) search(w http.ResponseWriter, r *http.Request) {
	q, typ := r.URL.Query().Get("q"), r.URL.Query().Get("type")
	if len(q) > 500 || len(typ) > 200 {
		fail(w, 400, "Слишком длинный поисковый запрос.", nil)
		return
	}
	res, err := a.store.Search(r.Context(), q, typ)
	if err != nil {
		a.dbError(w, err)
		return
	}
	respond(w, 200, res)
}

func decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		fail(w, 415, "Ожидается JSON.", nil)
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(dst); err != nil {
		fail(w, 400, "Неверные данные запроса или превышен размер 64 КиБ.", nil)
		return false
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		fail(w, 400, "Ожидается один JSON-объект.", nil)
		return false
	}
	return true
}

func (a *App) create(w http.ResponseWriter, r *http.Request) {
	var in assets.Input
	if !decode(w, r, &in) {
		return
	}
	result, err := a.store.Create(r.Context(), in)
	if err != nil {
		var ve *assets.ValidationError
		if errors.As(err, &ve) {
			fail(w, 422, "Проверьте заполненные поля.", ve.Fields)
			return
		}
		if errors.Is(err, store.ErrConflict) {
			fail(w, 409, "Оборудование с таким инвентарным номером уже есть в базе.", map[string]string{"inventory_number": "Инвентарный номер уже используется."})
			return
		}
		a.dbError(w, err)
		return
	}
	respond(w, 201, map[string]any{"asset": result})
}

type previewRequest struct {
	IDs []string `json:"ids"`
}

type documentRequest struct {
	IDs       []string          `json:"ids"`
	Revisions map[string]string `json:"revisions"`
	Number    string            `json:"number"`
	Date      string            `json:"date"`
	Recipient string            `json:"recipient"`
}

func validActText(s string, limit int) bool {
	if s == "" || !utf8.ValidString(s) || utf8.RuneCountInString(s) > limit {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) || r == 0xfffe || r == 0xffff {
			return false
		}
	}
	return true
}

func (a *App) document(w http.ResponseWriter, r *http.Request) {
	var in documentRequest
	if !decode(w, r, &in) {
		return
	}
	in.Number = strings.TrimSpace(in.Number)
	in.Recipient = strings.TrimSpace(in.Recipient)
	date, dateErr := time.Parse("2006-01-02", in.Date)
	if !validActText(in.Number, 100) || !validActText(in.Recipient, 500) || dateErr != nil || date.Year() < 1 || date.Year() > 9999 {
		fail(w, 422, "Укажите номер акта (до 100 символов), получателя (до 500 символов) и существующую дату.", nil)
		return
	}
	if err := validateIDs(in.IDs); err != nil {
		fail(w, 422, err.Error(), nil)
		return
	}
	if len(in.IDs) == 0 {
		fail(w, 422, "Добавьте оборудование в акт.", nil)
		return
	}
	if len(in.Revisions) != len(in.IDs) {
		fail(w, 409, "Обновите реквизиты выбранного оборудования перед формированием.", nil)
		return
	}
	items, err := a.store.ByIDs(r.Context(), in.IDs)
	if errors.Is(err, store.ErrNotFound) {
		fail(w, 409, "Одна из позиций больше не существует в базе. Проверьте список акта.", nil)
		return
	}
	if err != nil {
		a.dbError(w, err)
		return
	}
	for _, item := range items {
		if in.Revisions[item.ID] != item.Revision {
			fail(w, 409, "Реквизиты оборудования изменились. Обновите данные, проверьте изменения и повторите формирование.", nil)
			return
		}
	}
	if _, err := total(items); err != nil {
		fail(w, 422, err.Error(), nil)
		return
	}
	data, err := docgen.Generate(docgen.Act{Number: in.Number, Date: in.Date, Recipient: in.Recipient, Items: items})
	if err != nil {
		id := token()[:12]
		slog.Error("document_generation_failed", "error_id", id)
		fail(w, 500, "Не удалось сформировать Word. Повторите запрос или сообщите код ошибки: "+id, nil)
		return
	}
	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.wordprocessingml.document")
	w.Header().Set("Content-Disposition", `attachment; filename="act-`+in.Date+`.docx"`)
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func validateIDs(ids []string) error {
	if len(ids) > 100 {
		return errors.New("В один акт можно добавить не более 100 устройств.")
	}
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		n, e := strconv.ParseInt(id, 10, 64)
		if e != nil || n <= 0 || strconv.FormatInt(n, 10) != id {
			return errors.New("Неверный идентификатор оборудования.")
		}
		if seen[id] {
			return errors.New("Одно устройство нельзя добавить в акт дважды.")
		}
		seen[id] = true
	}
	return nil
}

func total(items []assets.Asset) (int64, error) {
	var n int64
	for _, v := range items {
		if v.PriceMinor < 0 || v.PriceMinor > math.MaxInt64-n {
			return 0, errors.New("Общая стоимость превышает допустимый диапазон.")
		}
		n += v.PriceMinor
	}
	return n, nil
}

func (a *App) preview(w http.ResponseWriter, r *http.Request) {
	var in previewRequest
	if !decode(w, r, &in) {
		return
	}
	if err := validateIDs(in.IDs); err != nil {
		fail(w, 422, err.Error(), nil)
		return
	}
	items, err := a.store.ByIDs(r.Context(), in.IDs)
	if errors.Is(err, store.ErrNotFound) {
		fail(w, 409, "Одна из позиций больше не существует в базе. Проверьте список акта.", nil)
		return
	}
	if err != nil {
		a.dbError(w, err)
		return
	}
	n, err := total(items)
	if err != nil {
		fail(w, 422, err.Error(), nil)
		return
	}
	if items == nil {
		items = []assets.Asset{}
	}
	respond(w, 200, map[string]any{"assets": items, "total_rub": money.Decimal(n), "total_display": money.Format(n)})
}
func (a *App) dbError(w http.ResponseWriter, err error) {
	id := token()[:12]
	slog.Error("database_request_failed", "error_id", id)
	fail(w, 503, "Не удалось получить данные из базы. Повторите запрос. Код ошибки: "+id, nil)
}
func fail(w http.ResponseWriter, status int, message string, fields map[string]string) {
	respond(w, status, map[string]any{"error": message, "fields": fields})
}
func respond(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
