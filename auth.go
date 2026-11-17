package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const (
	sessionCookie = "gp_session"
	sessionTTL    = 14 * 24 * time.Hour
)

var reUsername = regexp.MustCompile(`^[\p{L}\p{N}_.-]{2,40}$`)

func randomToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// upsertAdmin создаёт админа или меняет пароль существующему.
func (a *App) upsertAdmin(username, password string) error {
	if !reUsername.MatchString(username) {
		return errors.New("имя: 2–40 букв, цифр, знаков _ . -")
	}
	if len(password) < 8 || len(password) > 72 {
		return errors.New("пароль: от 8 до 72 символов")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	_, err = a.db.Exec(`INSERT INTO admins(username,password_hash,created_at) VALUES(?,?,?)
		ON CONFLICT(username) DO UPDATE SET password_hash=excluded.password_hash`,
		username, string(hash), time.Now().Unix())
	if err == nil { // смена пароля разлогинивает старые сессии
		_, err = a.db.Exec(`DELETE FROM sessions WHERE admin_id=(SELECT id FROM admins WHERE username=?)`, username)
	}
	return err
}

func (a *App) isAdmin(r *http.Request) bool {
	c, err := r.Cookie(sessionCookie)
	if err != nil || c.Value == "" {
		return false
	}
	var n int
	a.db.QueryRow(`SELECT COUNT(*) FROM sessions WHERE token=? AND expires_at>?`, c.Value, time.Now().Unix()).Scan(&n)
	return n == 1
}

func (a *App) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !a.isAdmin(r) {
			http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		next(w, r)
	}
}

func (a *App) loginForm(w http.ResponseWriter, r *http.Request) {
	if a.isAdmin(r) {
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
		return
	}
	a.render(w, r, http.StatusOK, "login", nil)
}

func (a *App) login(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<16)
	username, password := strings.TrimSpace(r.FormValue("username")), r.FormValue("password")
	var id int64
	var hash string
	err := a.db.QueryRow(`SELECT id,password_hash FROM admins WHERE username=?`, username).Scan(&id, &hash)
	if err != nil {
		// сравниваем с пустышкой, чтобы время ответа не выдавало существование имени
		hash = "$2a$10$7EqJtq98hPqEX7fNZaFWoOhi5BoRuJ8tC3nYB1B9T2V0Z3oG0oG0e"
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) != nil || err != nil {
		time.Sleep(time.Second) // тормозит перебор
		a.render(w, r, http.StatusUnauthorized, "login", map[string]any{"Error": "Неверное имя или пароль.", "Username": username})
		return
	}
	token := randomToken(32)
	now := time.Now()
	a.db.Exec(`DELETE FROM sessions WHERE expires_at<?`, now.Unix())
	if _, err := a.db.Exec(`INSERT INTO sessions(token,admin_id,expires_at) VALUES(?,?,?)`, token, id, now.Add(sessionTTL).Unix()); err != nil {
		http.Error(w, "Внутренняя ошибка", http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: token, Path: "/", HttpOnly: true,
		Secure: a.cfg.CookieSecure, SameSite: http.SameSiteStrictMode, MaxAge: int(sessionTTL.Seconds())})
	http.Redirect(w, r, "/admin", http.StatusSeeOther)
}

func (a *App) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		a.db.Exec(`DELETE FROM sessions WHERE token=?`, c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Path: "/", MaxAge: -1, HttpOnly: true,
		Secure: a.cfg.CookieSecure, SameSite: http.SameSiteStrictMode})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// internalRoutes — служебный API на отдельном порту. Он не проксируется наружу:
// в docker-compose порт привязан только к 127.0.0.1 сервера.
func (a *App) internalRoutes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /internal/admins", func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Username, Password string }
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&in); err != nil {
			http.Error(w, `ожидается JSON {"username":"…","password":"…"}`, http.StatusBadRequest)
			return
		}
		if err := a.upsertAdmin(in.Username, in.Password); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		fmt.Fprintf(w, "админ %q сохранён\n", in.Username)
	})
	return mux
}

// runCLI — команды внутри контейнера: `gazeta admin ИМЯ ПАРОЛЬ` и `gazeta healthcheck`.
// Обе ходят в уже запущенный сервер по локальному адресу.
func runCLI(cfg Config, args []string) int {
	local := func(addr string) string {
		if strings.HasPrefix(addr, ":") {
			return "http://127.0.0.1" + addr
		}
		return "http://" + addr
	}
	client := &http.Client{Timeout: 10 * time.Second}
	switch {
	case args[0] == "admin" && len(args) == 3:
		body, _ := json.Marshal(map[string]string{"username": args[1], "password": args[2]})
		resp, err := client.Post(local(cfg.InternalAddr)+"/internal/admins", "application/json", bytes.NewReader(body))
		if err != nil {
			fmt.Println("сервер недоступен:", err)
			return 1
		}
		defer resp.Body.Close()
		msg, _ := io.ReadAll(resp.Body)
		fmt.Print(string(msg))
		if resp.StatusCode != http.StatusOK {
			fmt.Println()
			return 1
		}
		return 0
	case args[0] == "healthcheck":
		resp, err := client.Get(local(cfg.Addr) + "/healthz")
		if err != nil || resp.StatusCode != http.StatusOK {
			return 1
		}
		return 0
	}
	fmt.Println("использование: gazeta admin ИМЯ ПАРОЛЬ | gazeta healthcheck")
	return 2
}
