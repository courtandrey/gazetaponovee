package main

import (
	"bytes"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestRenderBody(t *testing.T) {
	imgs := []Image{{1, "a.jpg"}, {2, "b.jpg"}, {3, "c.jpg"}}
	got := string(renderBody("# Шапка\n\nПривет, **мир** и *все*!\n<script>x</script>\n\n[фото 3]\n\n> цитата\n\n- раз\n- два\n\n[сюда](https://example.com/a?b=1&c=2)", imgs))
	for _, want := range []string{
		"<h2>Шапка</h2>", "<strong>мир</strong>", "<em>все</em>", "&lt;script&gt;",
		"<blockquote><p>цитата</p></blockquote>", "<li>раз</li><li>два</li>",
		`<a href="https://example.com/a?b=1&amp;c=2" rel="nofollow noopener">сюда</a>`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("нет %q в\n%s", want, got)
		}
	}
	if strings.Contains(got, "a.jpg") {
		t.Error("обложка не должна попадать в текст")
	}
	if c, b := strings.Index(got, "c.jpg"), strings.Index(got, "b.jpg"); c < 0 || b < 0 || c > b {
		t.Errorf("фото 3 должно стоять на своём месте, фото 2 — в конце: %s", got)
	}
	if strings.Contains(string(renderBody("[x](javascript:alert(1))", nil)), "<a") {
		t.Error("ссылка не на http(s) не должна становиться ссылкой")
	}
}

func TestExcerpt(t *testing.T) {
	if got := excerpt("# Заголовок\n\n**Жирно** и [ссылка](https://x.y) [фото 2]", 100); got != "Жирно и ссылка" {
		t.Errorf("получили %q", got)
	}
}

var png = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x06\x00\x00\x00\x1f\x15\xc4\x89")

func multipartBody(t *testing.T, fields map[string]string, fileName string, file []byte) (*bytes.Buffer, string) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for k, v := range fields {
		mw.WriteField(k, v)
	}
	if file != nil {
		fw, _ := mw.CreateFormFile("images", fileName)
		fw.Write(file)
	}
	mw.Close()
	return &buf, mw.FormDataContentType()
}

// Сквозной сценарий: материал от читателя → модерация → лента → выборы.
func TestFlow(t *testing.T) {
	app, err := newApp(Config{DataDir: t.TempDir(), MaxImageBytes: 1 << 20, MaxImages: 3, Motto: "девиз"})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(app.routes())
	defer srv.Close()
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	get := func(path string) (int, string) {
		resp, err := client.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	post := func(path string, form url.Values) (int, string) {
		resp, err := client.PostForm(srv.URL+path, form)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}

	// читатель присылает материал с картинкой
	body, ctype := multipartBody(t, map[string]string{"title": "Драконья руда найдена", "body": "Первый абзац.\n\nВторой.", "author": "Очевидец", "rubric": "ekonomika"}, "x.png", png)
	resp, err := client.Post(srv.URL+"/submit", ctype, body)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("отправка: %v %v", err, resp.StatusCode)
	}
	if _, html := get("/"); !strings.Contains(html, "Здесь пока пусто") {
		t.Error("пустая лента без приглашения прислать материал")
	}
	// не картинка отклоняется
	body, ctype = multipartBody(t, map[string]string{"title": "Т", "body": "Т"}, "x.png", []byte("<html>не картинка</html>"))
	if resp, _ := client.Post(srv.URL+"/submit", ctype, body); resp.StatusCode != 422 {
		t.Errorf("поддельная картинка: код %d", resp.StatusCode)
	}
	if _, html := get("/"); strings.Contains(html, "Драконья руда") {
		t.Fatal("материал виден до модерации")
	}
	if code, _ := get("/a/1"); code != 404 {
		t.Errorf("черновик доступен читателю: %d", code)
	}

	// без входа админка закрыта, действия не выполняются
	if _, html := get("/admin"); !strings.Contains(html, "Вход для редакции") {
		t.Error("админка открыта без входа")
	}
	post("/admin/a/1/quick", url.Values{"action": {"publish"}})
	if _, html := get("/"); strings.Contains(html, "Драконья руда") {
		t.Fatal("публикация без входа")
	}

	if err := app.upsertAdmin("murdok", "correct horse"); err != nil {
		t.Fatal(err)
	}
	if code, _ := post("/admin/login", url.Values{"username": {"murdok"}, "password": {"wrong"}}); code != 401 {
		t.Errorf("неверный пароль: код %d", code)
	}
	post("/admin/login", url.Values{"username": {"murdok"}, "password": {"correct horse"}})
	if _, html := get("/admin"); !strings.Contains(html, "Драконья руда") {
		t.Fatal("материал не попал в очередь")
	}
	post("/admin/a/1/quick", url.Values{"action": {"publish"}})
	post("/admin/a/1/quick", url.Values{"action": {"pin"}})
	_, html := get("/")
	if !strings.Contains(html, "Драконья руда") || !strings.Contains(html, "Главное") || !strings.Contains(html, "/uploads/") {
		t.Errorf("опубликованный закреплённый материал не на главной")
	}
	if strings.Contains(html, " года") {
		t.Error("на главной не должно быть пожара и календарных дат")
	}
	if _, html := get("/a/1"); !strings.Contains(html, "Очевидец") || !strings.Contains(html, "<p>Второй.</p>") {
		t.Error("страница материала неполная")
	}

	// выборы: скрыты, пока не включён флаг
	client.Jar, _ = cookiejar.New(nil)
	if code, _ := get("/elections"); code != 404 {
		t.Errorf("скрытые выборы отдают %d", code)
	}
	client.Jar = jar
	post("/admin/elections/candidate", url.Values{"name": {"Алый"}, "party": {"Старый порядок"}, "color": {"#9e1b32"}})
	post("/admin/elections/candidate", url.Values{"name": {"Синий"}, "color": {"#1f4e9c"}})
	for _, s := range []string{"delta", "pereprava", "dolina"} { // 22+21+13 = 56
		post("/admin/elections/call", url.Values{"state": {s}, "candidate": {"1"}})
	}
	post("/admin/elections/call", url.Values{"state": {"rubezh"}, "candidate": {"2"}})
	post("/admin/elections/senate", url.Values{"state": {"gryada"}, "contenders": {"А, Б"}})
	post("/admin/elections/senate", url.Values{"id": {"1"}, "state": {"gryada"}, "winner": {"А"}, "party": {"П"}, "color": {"#112233"}, "action": {"save"}})
	post("/admin/elections/settings", url.Values{"visible": {"on"}, "title": {"Ночь выборов"}})

	v := app.electionView()
	if v.Candidates[0].Votes != 56 || v.Candidates[1].Votes != 4 || v.Uncalled != 51 || v.Winner == nil || v.Winner.Name != "Алый" {
		t.Errorf("счёт: %+v %+v не объявлено %d", v.Candidates[0], v.Candidates[1], v.Uncalled)
	}
	client.Jar, _ = cookiejar.New(nil)
	code, html := get("/elections")
	if code != 200 || !strings.Contains(html, "Ночь выборов") || !strings.Contains(html, "fill:#9e1b32") || !strings.Contains(html, "Президентом избран Алый") || !strings.Contains(html, "Выборы в Сенат") {
		t.Errorf("страница выборов: код %d", code)
	}
	// снятие объявления
	client.Jar = jar
	post("/admin/elections/call", url.Values{"state": {"delta"}, "candidate": {"0"}})
	if v := app.electionView(); v.Candidates[0].Votes != 34 || v.Winner != nil {
		t.Errorf("после снятия Дельты: %d", v.Candidates[0].Votes)
	}

	// межсайтовый POST отсекается
	req, _ := http.NewRequest("POST", srv.URL+"/admin/elections/reset", nil)
	req.Header.Set("Origin", "https://evil.example")
	if resp, _ := client.Do(req); resp.StatusCode != 403 {
		t.Errorf("чужой Origin: код %d", resp.StatusCode)
	}
}

func TestStateTotals(t *testing.T) {
	sum := 0
	for _, s := range stateDefs {
		sum += s.Electors
	}
	if sum != totalElectors || len(stateDefs) != 9 {
		t.Errorf("выборщиков %d, штатов %d", sum, len(stateDefs))
	}
}
