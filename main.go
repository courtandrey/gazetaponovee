// Газета Поновее — маленький новостной сайт: лента, приём материалов, админка, карта выборов.
package main

import (
	"bytes"
	"database/sql"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata"

	_ "modernc.org/sqlite"
)

//go:embed templates/*.html static mapdata.json
var assets embed.FS

type Config struct {
	Addr          string // публичный адрес (за Caddy)
	InternalAddr  string // служебный API, наружу не публикуется
	DataDir       string
	Motto         string
	MaxImageBytes int64
	MaxImages     int
	CookieSecure  bool
}

type App struct {
	cfg     Config
	db      *sql.DB
	tpl     map[string]*template.Template
	geo     MapData
	version string
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if n, err := strconv.Atoi(os.Getenv(key)); err == nil && n > 0 {
		return n
	}
	return def
}

func loadConfig() Config {
	return Config{
		Addr:          env("ADDR", ":8080"),
		InternalAddr:  env("INTERNAL_ADDR", ":9090"),
		DataDir:       env("DATA_DIR", "/data"),
		Motto:         env("SITE_MOTTO", "Традиция, порядок и правда Девяти Штатов"),
		MaxImageBytes: int64(envInt("MAX_IMAGE_MB", 5)) << 20,
		MaxImages:     envInt("MAX_IMAGES", 6),
		CookieSecure:  env("COOKIE_SECURE", "true") != "false",
	}
}

const schema = `
CREATE TABLE IF NOT EXISTS admins(
	id INTEGER PRIMARY KEY, username TEXT UNIQUE NOT NULL, password_hash TEXT NOT NULL, created_at INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS sessions(
	token TEXT PRIMARY KEY, admin_id INTEGER NOT NULL REFERENCES admins(id) ON DELETE CASCADE, expires_at INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS articles(
	id INTEGER PRIMARY KEY, title TEXT NOT NULL, body TEXT NOT NULL, author TEXT NOT NULL DEFAULT '',
	rubric TEXT NOT NULL DEFAULT '', status TEXT NOT NULL DEFAULT 'pending',
	pinned INTEGER NOT NULL DEFAULT 0, breaking INTEGER NOT NULL DEFAULT 0,
	created_at INTEGER NOT NULL, published_at INTEGER);
CREATE INDEX IF NOT EXISTS articles_feed ON articles(status, published_at DESC);
CREATE TABLE IF NOT EXISTS images(
	id INTEGER PRIMARY KEY, article_id INTEGER NOT NULL REFERENCES articles(id) ON DELETE CASCADE, file TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS settings(key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS candidates(
	id INTEGER PRIMARY KEY, name TEXT NOT NULL, party TEXT NOT NULL DEFAULT '', color TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS state_calls(
	state TEXT PRIMARY KEY, candidate_id INTEGER NOT NULL REFERENCES candidates(id) ON DELETE CASCADE, called_at INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS senate_races(
	id INTEGER PRIMARY KEY, state TEXT NOT NULL, contenders TEXT NOT NULL DEFAULT '',
	winner TEXT NOT NULL DEFAULT '', party TEXT NOT NULL DEFAULT '', color TEXT NOT NULL DEFAULT '', called_at INTEGER);
CREATE TABLE IF NOT EXISTS transfers(
	id INTEGER PRIMARY KEY, at INTEGER NOT NULL, kind TEXT NOT NULL, src TEXT NOT NULL, src_role TEXT NOT NULL DEFAULT '',
	dst TEXT NOT NULL, dst_role TEXT NOT NULL DEFAULT '', amount INTEGER NOT NULL, note TEXT NOT NULL DEFAULT '');
`

func newApp(cfg Config) (*App, error) {
	if err := os.MkdirAll(filepath.Join(cfg.DataDir, "uploads"), 0o755); err != nil {
		return nil, err
	}
	// В образе scratch нет /tmp, а большие загрузки Go складывает во временные файлы.
	tmp := filepath.Join(cfg.DataDir, "tmp")
	os.RemoveAll(tmp)
	if err := os.MkdirAll(tmp, 0o700); err != nil {
		return nil, err
	}
	os.Setenv("TMPDIR", tmp)
	dsn := "file:" + filepath.Join(cfg.DataDir, "gazeta.db") +
		"?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // SQLite: один писатель, сайту этого хватает
	if _, err := db.Exec(schema); err != nil {
		return nil, fmt.Errorf("схема БД: %w", err)
	}
	a := &App{cfg: cfg, db: db, tpl: map[string]*template.Template{}, version: strconv.FormatInt(time.Now().Unix(), 36)}

	raw, err := assets.ReadFile("mapdata.json")
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(raw, &a.geo); err != nil {
		return nil, fmt.Errorf("mapdata.json: %w", err)
	}

	pages, err := assets.ReadDir("templates")
	if err != nil {
		return nil, err
	}
	for _, p := range pages {
		if p.Name() == "layout.html" {
			continue
		}
		t, err := template.New("").Funcs(tplFuncs).ParseFS(assets, "templates/layout.html", "templates/"+p.Name())
		if err != nil {
			return nil, err
		}
		a.tpl[strings.TrimSuffix(p.Name(), ".html")] = t
	}
	return a, nil
}

func (a *App) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", a.home)
	mux.HandleFunc("GET /r/{slug}", a.home)
	mux.HandleFunc("GET /a/{id}", a.article)
	mux.HandleFunc("GET /submit", a.submitForm)
	mux.HandleFunc("POST /submit", a.submit)
	mux.HandleFunc("GET /elections", a.elections)
	mux.HandleFunc("GET /endorsements", a.endorsements)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, "ok") })

	mux.Handle("GET /static/", cached(http.FileServerFS(assets)))
	uploads := http.StripPrefix("/uploads/", http.FileServer(http.Dir(filepath.Join(a.cfg.DataDir, "uploads"))))
	mux.Handle("GET /uploads/{file}", cached(uploads))

	mux.HandleFunc("GET /admin/login", a.loginForm)
	mux.HandleFunc("POST /admin/login", a.login)
	mux.HandleFunc("POST /admin/logout", a.logout)
	mux.HandleFunc("GET /admin", a.auth(a.adminList))
	mux.HandleFunc("GET /admin/a/{id}", a.auth(a.adminEdit))
	mux.HandleFunc("POST /admin/a/{id}", a.auth(a.adminSave))
	mux.HandleFunc("POST /admin/a/{id}/quick", a.auth(a.adminQuick))
	mux.HandleFunc("GET /admin/elections", a.auth(a.adminElections))
	mux.HandleFunc("POST /admin/elections/{what}", a.auth(a.adminElectionsPost))

	// Отсекает межсайтовые POST-запросы (CSRF) по Sec-Fetch-Site / Origin.
	return securityHeaders(http.NewCrossOriginProtection().Handler(mux))
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; frame-ancestors 'none'")
		next.ServeHTTP(w, r)
	})
}

func cached(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=604800")
		next.ServeHTTP(w, r)
	})
}

// render собирает страницу в буфер, чтобы ошибка шаблона не отдала половину HTML.
func (a *App) render(w http.ResponseWriter, r *http.Request, status int, page string, data map[string]any) {
	if data == nil {
		data = map[string]any{}
	}
	data["V"] = a.version
	data["Motto"] = a.cfg.Motto
	data["Rubrics"] = rubrics
	data["IsAdmin"] = a.isAdmin(r)
	data["ElectionOn"] = a.setting("election_visible") == "1"
	data["Urgent"] = a.listArticles("a.status='published' AND a.breaking=1", 3, 0)
	data["MaxImages"] = a.cfg.MaxImages
	data["MaxImageMB"] = a.cfg.MaxImageBytes >> 20

	var buf bytes.Buffer
	if err := a.tpl[page].ExecuteTemplate(&buf, "layout", data); err != nil {
		log.Printf("шаблон %s: %v", page, err)
		http.Error(w, "Внутренняя ошибка", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	buf.WriteTo(w)
}

func (a *App) notFound(w http.ResponseWriter, r *http.Request) {
	a.render(w, r, http.StatusNotFound, "message", map[string]any{
		"Heading": "Такой страницы в газете нет",
		"Text":    "Возможно, материал сняли с публикации или ссылка устарела.",
	})
}

func main() {
	log.SetFlags(log.LstdFlags)
	cfg := loadConfig()
	if len(os.Args) > 1 {
		os.Exit(runCLI(cfg, os.Args[1:]))
	}
	app, err := newApp(cfg)
	if err != nil {
		log.Fatal(err)
	}
	go func() {
		srv := &http.Server{Addr: cfg.InternalAddr, Handler: app.internalRoutes(), ReadHeaderTimeout: 5 * time.Second}
		log.Printf("служебный API на %s", cfg.InternalAddr)
		log.Fatal(srv.ListenAndServe())
	}()
	srv := &http.Server{Addr: cfg.Addr, Handler: app.routes(), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute}
	log.Printf("сайт на %s", cfg.Addr)
	log.Fatal(srv.ListenAndServe())
}
