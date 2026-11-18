package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParseTransfer(t *testing.T) {
	got, ok := parseTransfer("♦️ Деньги: Грег Нобель (CEO, Корпорация Г.Н.О.М.) → Мария Риордан (CEO, «Эссенциале»), 2\n«Инвестиционный раунд»")
	want := Transfer{Kind: kindMoney, From: "Грег Нобель", FromRole: "CEO, Корпорация Г.Н.О.М.",
		To: "Мария Риордан", ToRole: "CEO, «Эссенциале»", Amount: 2, Note: "Инвестиционный раунд"}
	if !ok || got != want {
		t.Errorf("получили %+v", got)
	}
	got, ok = parseTransfer("❤️ Популярность: Ториэль → Теодлуин «Тео» Аразорвин (сенатор, Дельта), 1\n«♥️»")
	if !ok || got.Kind != kindPop || got.From != "Ториэль" || got.FromRole != "" || got.To != "Теодлуин «Тео» Аразорвин" || got.Amount != 1 || got.Note != "♥️" {
		t.Errorf("получили %+v", got)
	}
	for _, s := range []string{"В этот чат ничего не пишем", "❤️ Популярность: кто-то, 1", ""} {
		if _, ok := parseTransfer(s); ok {
			t.Errorf("%q не перевод", s)
		}
	}
}

func TestEndorsements(t *testing.T) {
	t.Setenv("TMPDIR", "") // newApp из прошлого теста перенаправил TMPDIR в уже удалённый каталог
	app, err := newApp(Config{DataDir: t.TempDir(), Motto: "девиз"})
	if err != nil {
		t.Fatal(err)
	}
	internal := httptest.NewServer(app.internalRoutes())
	defer internal.Close()
	site := httptest.NewServer(app.routes())
	defer site.Close()
	sync := func(body string) string {
		resp, err := http.Post(internal.URL+"/internal/endorsements", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return string(b)
	}
	get := func(path string) string {
		resp, err := http.Get(site.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return string(b)
	}

	if html := get("/endorsements"); !strings.Contains(html, "Переводов пока не было") {
		t.Error("пустая страница")
	}
	msg := sync(`{"messages":[
		{"id":1,"date":1795000000,"text":"❤️ Популярность: Герман Бридж → Грег Нобель, 1\n«Спасибо за ГНОМа»"},
		{"id":2,"date":1795000100,"text":"❤️ Популярность: Грег Нобель → Феррдо Руджа, 2\n«Ф. Руджа»"},
		{"id":3,"date":1795000200,"text":"♦️ Деньги: Грег Нобель (CEO, Корпорация Г.Н.О.М.) → Герман Бридж, 3\n«<b>займ</b>»"},
		{"id":4,"date":1795000300,"text":"просьба не флудить"}]}`)
	if !strings.Contains(msg, "переводов: 3, пропущено сообщений: 1") {
		t.Errorf("ответ: %s", msg)
	}

	html := get("/endorsements")
	for _, want := range []string{`marker-end="url(#arr-pop)"`, `marker-end="url(#arr-money)"`, "Феррдо Руджа",
		"&lt;b&gt;займ&lt;/b&gt;", "/endorsements?who=%d0%93%d1%80%d0%b5%d0%b3", "последнее обновление"} {
		if !strings.Contains(html, want) {
			t.Errorf("нет %q", want)
		}
	}

	v := app.graphView("", "Грег Нобель")
	if v.Selected == nil || v.Selected.Role != "CEO, Корпорация Г.Н.О.М." || v.Selected.Got != [2]int{1, 0} || v.Selected.Gave != [2]int{2, 3} || len(v.Log) != 3 {
		t.Errorf("выбран Грег: %+v, записей %d", v.Selected, len(v.Log))
	}
	for _, n := range v.Nodes {
		if n.X < 0 || n.X > float64(v.W) || n.Y < 0 || n.Y > float64(v.H) || strings.Contains(n.Class, "dim") {
			t.Errorf("узел %s: (%v,%v) %s", n.Name, n.X, n.Y, n.Class)
		}
	}
	if v := app.graphView("money", "Феррдо Руджа"); len(v.Log) != 0 {
		t.Errorf("у Руджи денежных переводов нет: %d", len(v.Log))
	}
	if html := get("/endorsements?who=Грег+Нобель&k=money"); !strings.Contains(html, `class="node sel"`) || !strings.Contains(html, "Переводы участника") {
		t.Error("страница участника")
	}

	// новый снимок без удалённых сообщений заменяет старый
	sync(`{"messages":[{"id":2,"date":1795000100,"text":"❤️ Популярность: Грег Нобель → Феррдо Руджа, 2"}]}`)
	if v := app.graphView("", ""); len(v.Nodes) != 2 || len(v.Log) != 1 {
		t.Errorf("после замены: узлов %d, записей %d", len(v.Nodes), len(v.Log))
	}
	if msg := sync(`{"nope":1}`); !strings.Contains(msg, "ожидается JSON") {
		t.Errorf("кривой запрос: %s", msg)
	}
}
