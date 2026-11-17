package main

import (
	"fmt"
	"html"
	"html/template"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type Rubric struct{ Slug, Name string }

var rubrics = []Rubric{
	{"politika", "Политика"},
	{"shtaty", "Штаты"},
	{"ekonomika", "Экономика"},
	{"magiya", "Магия"},
	{"mir", "За рубежом"},
	{"mneniya", "Мнения"},
}

func rubricName(slug string) string {
	for _, r := range rubrics {
		if r.Slug == slug {
			return r.Name
		}
	}
	return ""
}

var (
	months   = [...]string{"января", "февраля", "марта", "апреля", "мая", "июня", "июля", "августа", "сентября", "октября", "ноября", "декабря"}
	weekdays = [...]string{"Воскресенье", "Понедельник", "Вторник", "Среда", "Четверг", "Пятница", "Суббота"}
)

func dayLabel(t time.Time) string {
	return fmt.Sprintf("%d %s %d года", t.Day(), months[t.Month()-1], t.Year())
}
func longDate(t time.Time) string { return weekdays[t.Weekday()] + ", " + dayLabel(t) }
func clock(t time.Time) string    { return t.Format("15:04") }
func stamp(t time.Time) string {
	return fmt.Sprintf("%d %s %d, %s", t.Day(), months[t.Month()-1], t.Year(), clock(t))
}

var tplFuncs = template.FuncMap{
	"rubric": rubricName,
	"day":    dayLabel,
	"clock":  clock,
	"stamp":  stamp,
	"inc":    func(i int) int { return i + 1 },
}

// --- Разметка материалов ---
// Поддерживается небольшое подмножество Markdown: «# заголовок», «## подзаголовок»,
// «> цитата», «- список», **жирный**, *курсив*, [ссылка](https://…) и [фото N].
// Весь текст сначала экранируется, поэтому HTML от авторов не проходит.

var (
	reBlocks = regexp.MustCompile(`\n[ \t]*\n`)
	reLink   = regexp.MustCompile(`\[([^\]\n]+)\]\((https?://[^\s)]+)\)`)
	reBold   = regexp.MustCompile(`\*\*([^*\n]+)\*\*`)
	reItalic = regexp.MustCompile(`\*([^*\n]+)\*`)
	rePhoto  = regexp.MustCompile(`(?i)^\[фото[: ]*(\d+)\]$`)
)

func inline(s string) string {
	s = html.EscapeString(s)
	s = reLink.ReplaceAllString(s, `<a href="$2" rel="nofollow noopener">$1</a>`)
	s = reBold.ReplaceAllString(s, `<strong>$1</strong>`)
	return reItalic.ReplaceAllString(s, `<em>$1</em>`)
}

func allPrefixed(lines []string, prefix string) bool {
	for _, l := range lines {
		if !strings.HasPrefix(l, prefix) {
			return false
		}
	}
	return true
}

func figure(file string) string {
	return `<figure><img src="/uploads/` + html.EscapeString(file) + `" alt="" loading="lazy"></figure>`
}

// renderBody превращает текст материала в HTML. Первое изображение — обложка, её
// выводит шаблон; остальные встают на место [фото N] либо в конец текста.
func renderBody(body string, images []Image) template.HTML {
	var b strings.Builder
	placed := map[int]bool{}
	text := strings.TrimSpace(strings.ReplaceAll(body, "\r\n", "\n"))
	for _, block := range reBlocks.Split(text, -1) {
		block = strings.TrimSpace(block)
		if block == "" {
			continue
		}
		lines := strings.Split(block, "\n")
		for i := range lines {
			lines[i] = strings.TrimSpace(lines[i])
		}
		switch {
		case rePhoto.MatchString(block):
			n, _ := strconv.Atoi(rePhoto.FindStringSubmatch(block)[1])
			if n >= 2 && n <= len(images) && !placed[n] {
				placed[n] = true
				b.WriteString(figure(images[n-1].File))
			}
		case strings.HasPrefix(block, "## "):
			b.WriteString("<h3>" + inline(strings.Join(lines, " ")[3:]) + "</h3>")
		case strings.HasPrefix(block, "# "):
			b.WriteString("<h2>" + inline(strings.Join(lines, " ")[2:]) + "</h2>")
		case allPrefixed(lines, ">"):
			for i, l := range lines {
				lines[i] = inline(strings.TrimSpace(l[1:]))
			}
			b.WriteString("<blockquote><p>" + strings.Join(lines, "<br>") + "</p></blockquote>")
		case allPrefixed(lines, "- "):
			b.WriteString("<ul>")
			for _, l := range lines {
				b.WriteString("<li>" + inline(l[2:]) + "</li>")
			}
			b.WriteString("</ul>")
		default:
			for i, l := range lines {
				lines[i] = inline(l)
			}
			b.WriteString("<p>" + strings.Join(lines, "<br>") + "</p>")
		}
	}
	for n := 2; n <= len(images); n++ {
		if !placed[n] {
			b.WriteString(figure(images[n-1].File))
		}
	}
	return template.HTML(b.String())
}

var (
	reExcerptHead  = regexp.MustCompile(`(?m)^\s*#{1,2} .*$`)
	reExcerptPhoto = regexp.MustCompile(`(?i)\[фото[: ]*\d+\]`)
	reExcerptLink  = regexp.MustCompile(`\[([^\]\n]+)\]\([^)\s]+\)`)
	reExcerptMarks = regexp.MustCompile(`(?m)^\s*(#{1,2}|>|-)\s+|\*+`)
)

// excerpt — начало текста без разметки для ленты.
func excerpt(body string, limit int) string {
	s := reExcerptHead.ReplaceAllString(body, "")
	s = reExcerptPhoto.ReplaceAllString(s, "")
	s = reExcerptLink.ReplaceAllString(s, "$1")
	s = reExcerptMarks.ReplaceAllString(s, "")
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) <= limit {
		return s
	}
	cut := string(r[:limit])
	if i := strings.LastIndex(cut, " "); i > limit/2 {
		cut = cut[:i]
	}
	return strings.TrimRight(cut, ",.;:—- ") + "…"
}
