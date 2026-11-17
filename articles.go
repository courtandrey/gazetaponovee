package main

import (
	"errors"
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
	"unicode/utf8"
)

const (
	pageSize     = 20
	maxTitleLen  = 200
	maxAuthorLen = 80
	maxBodyLen   = 50000
)

type Image struct {
	ID   int64
	File string
}

type Article struct {
	ID          int64
	Title       string
	Body        string
	Author      string
	Rubric      string
	Status      string // pending | published | rejected
	Pinned      bool
	Breaking    bool
	CreatedAt   time.Time
	PublishedAt time.Time // нулевое, пока не опубликован
	Cover       string
	Images      []Image
}

func (a Article) Excerpt() string { return excerpt(a.Body, 260) }
func (a Article) Byline() string {
	if a.Author == "" {
		return "Редакция"
	}
	return a.Author
}
func (a Article) HTML() template.HTML { return renderBody(a.Body, a.Images) }

const articleCols = `a.id,a.title,a.body,a.author,a.rubric,a.status,a.pinned,a.breaking,a.created_at,
	COALESCE(a.published_at,0),COALESCE((SELECT file FROM images WHERE article_id=a.id ORDER BY id LIMIT 1),'')`

type scanner interface{ Scan(...any) error }

func scanArticle(row scanner) (Article, error) {
	var a Article
	var created, published int64
	err := row.Scan(&a.ID, &a.Title, &a.Body, &a.Author, &a.Rubric, &a.Status, &a.Pinned, &a.Breaking, &created, &published, &a.Cover)
	a.CreatedAt = time.Unix(created, 0)
	if published > 0 {
		a.PublishedAt = time.Unix(published, 0)
	}
	return a, err
}

func (a *App) listArticles(where string, limit, offset int, args ...any) []Article {
	args = append(args, limit, offset)
	rows, err := a.db.Query(`SELECT `+articleCols+` FROM articles a WHERE `+where+
		` ORDER BY COALESCE(a.published_at,a.created_at) DESC, a.id DESC LIMIT ? OFFSET ?`, args...)
	if err != nil {
		log.Printf("список материалов: %v", err)
		return nil
	}
	defer rows.Close()
	var out []Article
	for rows.Next() {
		if art, err := scanArticle(rows); err == nil {
			out = append(out, art)
		}
	}
	return out
}

func (a *App) getArticle(id int64) (Article, error) {
	art, err := scanArticle(a.db.QueryRow(`SELECT `+articleCols+` FROM articles a WHERE a.id=?`, id))
	if err != nil {
		return art, err
	}
	rows, err := a.db.Query(`SELECT id,file FROM images WHERE article_id=? ORDER BY id`, id)
	if err != nil {
		return art, err
	}
	defer rows.Close()
	for rows.Next() {
		var im Image
		rows.Scan(&im.ID, &im.File)
		art.Images = append(art.Images, im)
	}
	return art, nil
}

func (a *App) setting(key string) string {
	var v string
	a.db.QueryRow(`SELECT value FROM settings WHERE key=?`, key).Scan(&v)
	return v
}

func (a *App) setSetting(key, value string) {
	a.db.Exec(`INSERT INTO settings(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
}

// --- Изображения ---

var imageExt = map[string]string{"image/jpeg": ".jpg", "image/png": ".png", "image/gif": ".gif", "image/webp": ".webp"}

func (a *App) uploadPath(file string) string {
	return filepath.Join(a.cfg.DataDir, "uploads", filepath.Base(file))
}

// saveImages сохраняет файлы из поля images. Тип определяется по содержимому, а не по имени.
func (a *App) saveImages(r *http.Request) (files []string, err error) {
	if r.MultipartForm == nil {
		return nil, nil
	}
	defer func() {
		if err != nil {
			for _, f := range files {
				os.Remove(a.uploadPath(f))
			}
			files = nil
		}
	}()
	for _, fh := range r.MultipartForm.File["images"] {
		if fh.Size == 0 {
			continue
		}
		if len(files) == a.cfg.MaxImages {
			return files, fmt.Errorf("Можно приложить не больше %d изображений.", a.cfg.MaxImages)
		}
		if fh.Size > a.cfg.MaxImageBytes {
			return files, fmt.Errorf("Файл «%s» больше %d МБ.", fh.Filename, a.cfg.MaxImageBytes>>20)
		}
		src, err := fh.Open()
		if err != nil {
			return files, errors.New("Не удалось прочитать файл.")
		}
		head := make([]byte, 512)
		n, _ := io.ReadFull(src, head)
		ext, ok := imageExt[http.DetectContentType(head[:n])]
		if !ok {
			src.Close()
			return files, fmt.Errorf("Файл «%s» не похож на изображение. Подойдут JPEG, PNG, GIF и WebP.", fh.Filename)
		}
		name := randomToken(12) + ext
		dst, err := os.OpenFile(a.uploadPath(name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err != nil {
			src.Close()
			return files, errors.New("Не удалось сохранить файл.")
		}
		files = append(files, name)
		dst.Write(head[:n])
		_, err = io.Copy(dst, src)
		src.Close()
		if cerr := dst.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return files, errors.New("Не удалось сохранить файл.")
		}
	}
	return files, nil
}

func (a *App) attachImages(articleID int64, files []string) {
	for _, f := range files {
		a.db.Exec(`INSERT INTO images(article_id,file) VALUES(?,?)`, articleID, f)
	}
}

// limitUpload ограничивает размер запроса и разбирает форму.
func (a *App) limitUpload(w http.ResponseWriter, r *http.Request) error {
	r.Body = http.MaxBytesReader(w, r.Body, int64(a.cfg.MaxImages)*a.cfg.MaxImageBytes+(2<<20))
	err := r.ParseMultipartForm(4 << 20)
	if errors.Is(err, http.ErrNotMultipart) {
		return r.ParseForm()
	}
	return err
}

type articleInput struct{ Title, Body, Author, Rubric string }

func readArticleInput(r *http.Request) (articleInput, error) {
	in := articleInput{
		Title:  strings.Join(strings.Fields(r.FormValue("title")), " "),
		Body:   strings.TrimSpace(r.FormValue("body")),
		Author: strings.Join(strings.Fields(r.FormValue("author")), " "),
		Rubric: r.FormValue("rubric"),
	}
	if rubricName(in.Rubric) == "" {
		in.Rubric = ""
	}
	switch {
	case in.Title == "":
		return in, errors.New("Напишите заголовок.")
	case utf8.RuneCountInString(in.Title) > maxTitleLen:
		return in, fmt.Errorf("Заголовок длиннее %d знаков.", maxTitleLen)
	case in.Body == "":
		return in, errors.New("Напишите текст материала.")
	case utf8.RuneCountInString(in.Body) > maxBodyLen:
		return in, fmt.Errorf("Текст длиннее %d знаков.", maxBodyLen)
	case utf8.RuneCountInString(in.Author) > maxAuthorLen:
		return in, fmt.Errorf("Подпись длиннее %d знаков.", maxAuthorLen)
	}
	return in, nil
}

// --- Публичные страницы ---

func (a *App) home(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	if slug != "" && rubricName(slug) == "" {
		a.notFound(w, r)
		return
	}
	page, _ := strconv.Atoi(r.URL.Query().Get("p"))
	if page < 1 {
		page = 1
	}
	data := map[string]any{"Rubric": slug, "Page": page}

	// Закреплённые стоят наверху главной и в общую хронику не попадают.
	where, args := "a.status='published' AND a.pinned=0", []any{}
	if slug != "" {
		where, args = "a.status='published' AND a.rubric=?", []any{slug}
	} else if page == 1 {
		data["Pinned"] = a.listArticles("a.status='published' AND a.pinned=1", 12, 0)
		if a.setting("election_visible") == "1" {
			data["E"] = a.electionView()
		}
	}
	items := a.listArticles(where, pageSize+1, (page-1)*pageSize, args...)
	if len(items) > pageSize {
		items = items[:pageSize]
		data["Next"] = page + 1
	}
	if page > 1 {
		data["Prev"] = page - 1
	}
	data["Items"] = items
	a.render(w, r, http.StatusOK, "home", data)
}

func (a *App) article(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	art, err := a.getArticle(id)
	if err != nil || (art.Status != "published" && !a.isAdmin(r)) {
		a.notFound(w, r)
		return
	}
	a.render(w, r, http.StatusOK, "article", map[string]any{"A": art})
}

func (a *App) submitForm(w http.ResponseWriter, r *http.Request) {
	a.render(w, r, http.StatusOK, "submit", map[string]any{"In": articleInput{}})
}

func (a *App) submit(w http.ResponseWriter, r *http.Request) {
	fail := func(status int, in articleInput, msg string) {
		a.render(w, r, status, "submit", map[string]any{"In": in, "Error": msg})
	}
	if err := a.limitUpload(w, r); err != nil {
		fail(http.StatusRequestEntityTooLarge, articleInput{}, "Письмо слишком тяжёлое: уменьшите или уберите часть изображений.")
		return
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}
	in, err := readArticleInput(r)
	if err != nil {
		fail(http.StatusUnprocessableEntity, in, err.Error())
		return
	}
	files, err := a.saveImages(r)
	if err != nil {
		fail(http.StatusUnprocessableEntity, in, err.Error())
		return
	}
	res, err := a.db.Exec(`INSERT INTO articles(title,body,author,rubric,status,created_at) VALUES(?,?,?,?,'pending',?)`,
		in.Title, in.Body, in.Author, in.Rubric, time.Now().Unix())
	if err != nil {
		log.Printf("приём материала: %v", err)
		fail(http.StatusInternalServerError, in, "Не удалось сохранить материал. Попробуйте ещё раз.")
		return
	}
	id, _ := res.LastInsertId()
	a.attachImages(id, files)
	a.render(w, r, http.StatusOK, "message", map[string]any{
		"Heading": "Материал у редакции",
		"Text":    "Редактор прочтёт его и решит, публиковать ли. Опубликованные материалы появляются в ленте.",
		"Link":    "/submit", "LinkText": "Прислать ещё один",
	})
}

// --- Админка: материалы ---

func (a *App) adminList(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("s")
	if status != "published" && status != "rejected" {
		status = "pending"
	}
	counts := map[string]int{}
	rows, err := a.db.Query(`SELECT status,COUNT(*) FROM articles GROUP BY status`)
	if err == nil {
		for rows.Next() {
			var s string
			var n int
			rows.Scan(&s, &n)
			counts[s] = n
		}
		rows.Close()
	}
	a.render(w, r, http.StatusOK, "admin_list", map[string]any{
		"Status": status, "Counts": counts, "Items": a.listArticles("a.status=?", 300, 0, status),
	})
}

func (a *App) adminEdit(w http.ResponseWriter, r *http.Request) {
	art := Article{Status: "new"}
	if r.PathValue("id") != "new" {
		id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
		var err error
		if art, err = a.getArticle(id); err != nil {
			a.notFound(w, r)
			return
		}
	}
	a.render(w, r, http.StatusOK, "admin_edit", map[string]any{"A": art, "Saved": r.URL.Query().Get("saved") != ""})
}

func (a *App) deleteImageFiles(where string, args ...any) {
	rows, err := a.db.Query(`SELECT file FROM images WHERE `+where, args...)
	if err != nil {
		return
	}
	var files []string
	for rows.Next() {
		var f string
		rows.Scan(&f)
		files = append(files, f)
	}
	rows.Close()
	a.db.Exec(`DELETE FROM images WHERE `+where, args...)
	for _, f := range files {
		os.Remove(a.uploadPath(f))
	}
}

// applyAction меняет статус материала: publish | unpublish | reject | delete | pin | unpin.
func (a *App) applyAction(id int64, action string) {
	switch action {
	case "publish":
		a.db.Exec(`UPDATE articles SET status='published', published_at=COALESCE(published_at,?) WHERE id=?`, time.Now().Unix(), id)
	case "unpublish":
		a.db.Exec(`UPDATE articles SET status='pending' WHERE id=?`, id)
	case "reject":
		a.db.Exec(`UPDATE articles SET status='rejected', pinned=0, breaking=0 WHERE id=?`, id)
	case "pin", "unpin":
		a.db.Exec(`UPDATE articles SET pinned=? WHERE id=?`, action == "pin", id)
	case "delete":
		a.deleteImageFiles("article_id=?", id)
		a.db.Exec(`DELETE FROM articles WHERE id=?`, id)
	}
}

func (a *App) adminSave(w http.ResponseWriter, r *http.Request) {
	if err := a.limitUpload(w, r); err != nil {
		http.Error(w, "Запрос слишком большой: уменьшите изображения.", http.StatusRequestEntityTooLarge)
		return
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}
	isNew := r.PathValue("id") == "new"
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	action := r.FormValue("action")
	if action == "delete" && !isNew {
		a.applyAction(id, "delete")
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
		return
	}

	in, err := readArticleInput(r)
	var files []string
	if err == nil {
		files, err = a.saveImages(r)
	}
	if err != nil {
		art, _ := a.getArticle(id)
		if isNew {
			art.Status = "new"
		}
		art.Title, art.Body, art.Author, art.Rubric = in.Title, in.Body, in.Author, in.Rubric
		a.render(w, r, http.StatusUnprocessableEntity, "admin_edit", map[string]any{"A": art, "Error": err.Error()})
		return
	}
	pinned, breaking := r.FormValue("pinned") != "", r.FormValue("breaking") != ""
	if isNew {
		res, err := a.db.Exec(`INSERT INTO articles(title,body,author,rubric,status,pinned,breaking,created_at) VALUES(?,?,?,?,'pending',?,?,?)`,
			in.Title, in.Body, in.Author, in.Rubric, pinned, breaking, time.Now().Unix())
		if err != nil {
			http.Error(w, "Не удалось сохранить.", http.StatusInternalServerError)
			return
		}
		id, _ = res.LastInsertId()
	} else {
		res, err := a.db.Exec(`UPDATE articles SET title=?,body=?,author=?,rubric=?,pinned=?,breaking=? WHERE id=?`,
			in.Title, in.Body, in.Author, in.Rubric, pinned, breaking, id)
		if err != nil {
			http.Error(w, "Не удалось сохранить.", http.StatusInternalServerError)
			return
		}
		if n, _ := res.RowsAffected(); n == 0 {
			a.notFound(w, r)
			return
		}
		for _, v := range r.Form["del_image"] {
			imgID, _ := strconv.ParseInt(v, 10, 64)
			a.deleteImageFiles("id=? AND article_id=?", imgID, id)
		}
	}
	a.attachImages(id, files)
	if action == "publish" || action == "unpublish" || action == "reject" {
		a.applyAction(id, action)
	}
	http.Redirect(w, r, fmt.Sprintf("/admin/a/%d?saved=1", id), http.StatusSeeOther)
}

func (a *App) adminQuick(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	a.applyAction(id, r.FormValue("action"))
	back := r.FormValue("back")
	if back != "published" && back != "rejected" {
		back = "pending"
	}
	http.Redirect(w, r, "/admin?s="+back, http.StatusSeeOther)
}
