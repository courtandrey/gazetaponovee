package main

import (
	"fmt"
	"html/template"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// MapData — контуры штатов, снятые с map.jpg скриптом tools/trace_map.py.
type MapData struct {
	W, H   int
	States map[string]struct {
		D      string
		Cx, Cy int
	}
}

type StateDef struct {
	ID, Name string
	Electors int
	LX, LY   int     // где стоит подпись на карте
	Leader   [2]int  // если не ноль — конец выноски от подписи к штату
	Size     float64 // кегль подписи
}

// Выборщики — по текущей переписи (pol_system): всего 111, для победы нужно 56.
var stateDefs = []StateDef{
	{ID: "delta", Name: "Дельта", Electors: 22, LX: 778, LY: 322, Size: 15},
	{ID: "pereprava", Name: "Переправа", Electors: 21, LX: 752, LY: 452, Leader: [2]int{846, 384}, Size: 15},
	{ID: "poberezhe", Name: "Побережье", Electors: 18, LX: 893, LY: 478, Size: 19},
	{ID: "gryada", Name: "Гряда", Electors: 14, LX: 760, LY: 124, Size: 22},
	{ID: "dolina", Name: "Долина", Electors: 13, LX: 880, LY: 292, Size: 14},
	{ID: "razlom", Name: "Разлом", Electors: 8, LX: 795, LY: 205, Size: 19},
	{ID: "kanyon", Name: "Каньон", Electors: 7, LX: 540, LY: 290, Size: 20},
	{ID: "arhipelag", Name: "Архипелаг", Electors: 4, LX: 1085, LY: 508, Size: 16},
	{ID: "rubezh", Name: "Рубеж", Electors: 4, LX: 350, LY: 385, Size: 34},
}

const (
	totalElectors = 111
	electorsToWin = 56
)

func stateName(id string) string {
	for _, s := range stateDefs {
		if s.ID == id {
			return s.Name
		}
	}
	return ""
}

var reColor = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

// inkOn подбирает цвет текста, читаемый на заливке.
func inkOn(color string) string {
	if !reColor.MatchString(color) {
		return "#2a1338"
	}
	v, _ := strconv.ParseUint(color[1:], 16, 32)
	if lum := 0.299*float64(v>>16) + 0.587*float64(v>>8&0xff) + 0.114*float64(v&0xff); lum > 160 {
		return "#2a1338"
	}
	return "#ffffff"
}

func swatch(color string) template.CSS {
	return template.CSS("background:" + color + ";color:" + inkOn(color))
}

type Candidate struct {
	ID                 int64
	Name, Party, Color string
	Votes              int
	States             []string
}

func (c Candidate) Swatch() template.CSS { return swatch(c.Color) }

type StateView struct {
	StateDef
	D        string
	Fill     template.CSS // пусто, пока штат не объявлен
	Ink      template.CSS
	Winner   *Candidate
	CalledAt time.Time
}

type BarSeg struct {
	Style template.CSS
	Label string
}

type SenateRace struct {
	ID                               int64
	State, Contenders, Winner, Party string
	Color                            string
	CalledAt                         time.Time
}

func (s SenateRace) StateName() string    { return stateName(s.State) }
func (s SenateRace) Swatch() template.CSS { return swatch(s.Color) }

type ElectionView struct {
	Title      string
	Visible    bool
	W, H       int
	Candidates []*Candidate
	States     []StateView
	Calls      []StateView // объявленные штаты, свежие сверху
	Bar        []BarSeg
	Uncalled   int
	Total      int
	Need       int
	Winner     *Candidate
	Senate     []SenateRace
}

func (a *App) electionView() ElectionView {
	v := ElectionView{
		Title: a.setting("election_title"), Visible: a.setting("election_visible") == "1",
		W: a.geo.W, H: a.geo.H, Total: totalElectors, Need: electorsToWin, Uncalled: totalElectors,
	}
	if v.Title == "" {
		v.Title = "Выборы президента Девяти Штатов"
	}
	byID := map[int64]*Candidate{}
	if rows, err := a.db.Query(`SELECT id,name,party,color FROM candidates ORDER BY id`); err == nil {
		for rows.Next() {
			c := &Candidate{}
			rows.Scan(&c.ID, &c.Name, &c.Party, &c.Color)
			v.Candidates = append(v.Candidates, c)
			byID[c.ID] = c
		}
		rows.Close()
	}
	type call struct {
		cand int64
		at   int64
	}
	calls := map[string]call{}
	if rows, err := a.db.Query(`SELECT state,candidate_id,called_at FROM state_calls`); err == nil {
		for rows.Next() {
			var s string
			var c call
			rows.Scan(&s, &c.cand, &c.at)
			calls[s] = c
		}
		rows.Close()
	}
	for _, def := range stateDefs {
		sv := StateView{StateDef: def, D: a.geo.States[def.ID].D}
		if c, ok := calls[def.ID]; ok && byID[c.cand] != nil {
			w := byID[c.cand]
			w.Votes += def.Electors
			w.States = append(w.States, def.Name)
			v.Uncalled -= def.Electors
			sv.Winner, sv.CalledAt = w, time.Unix(c.at, 0)
			sv.Fill, sv.Ink = template.CSS("fill:"+w.Color), template.CSS("fill:"+inkOn(w.Color))
			v.Calls = append(v.Calls, sv)
		}
		v.States = append(v.States, sv)
	}
	sort.SliceStable(v.Calls, func(i, j int) bool { return v.Calls[i].CalledAt.After(v.Calls[j].CalledAt) })

	// Шкала как на американских картах: первый кандидат растёт слева, остальные справа,
	// необъявленные голоса — посередине.
	seg := func(n int, color, label string) {
		if n > 0 {
			style := fmt.Sprintf("flex:%d 0 0;", n)
			if color != "" {
				style += string(swatch(color))
			}
			v.Bar = append(v.Bar, BarSeg{Style: template.CSS(style), Label: label})
		}
	}
	for i, c := range v.Candidates {
		if c.Votes >= electorsToWin {
			v.Winner = c
		}
		if i == 0 {
			seg(c.Votes, c.Color, strconv.Itoa(c.Votes))
		}
	}
	seg(v.Uncalled, "", "")
	for i := len(v.Candidates) - 1; i > 0; i-- {
		seg(v.Candidates[i].Votes, v.Candidates[i].Color, strconv.Itoa(v.Candidates[i].Votes))
	}

	if rows, err := a.db.Query(`SELECT id,state,contenders,winner,party,color,COALESCE(called_at,0) FROM senate_races ORDER BY id`); err == nil {
		for rows.Next() {
			var s SenateRace
			var at int64
			rows.Scan(&s.ID, &s.State, &s.Contenders, &s.Winner, &s.Party, &s.Color, &at)
			if at > 0 {
				s.CalledAt = time.Unix(at, 0)
			}
			v.Senate = append(v.Senate, s)
		}
		rows.Close()
	}
	return v
}

func (a *App) elections(w http.ResponseWriter, r *http.Request) {
	v := a.electionView()
	if !v.Visible && !a.isAdmin(r) { // админ видит раздел заранее
		a.notFound(w, r)
		return
	}
	a.render(w, r, http.StatusOK, "elections", map[string]any{"E": v})
}

func (a *App) adminElections(w http.ResponseWriter, r *http.Request) {
	a.render(w, r, http.StatusOK, "admin_elections", map[string]any{"E": a.electionView(), "StateDefs": stateDefs})
}

func short(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

func (a *App) adminElectionsPost(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<16)
	id, _ := strconv.ParseInt(r.FormValue("id"), 10, 64)
	action := r.FormValue("action")
	name, party, color := short(r.FormValue("name"), 80), short(r.FormValue("party"), 80), r.FormValue("color")
	if !reColor.MatchString(color) {
		color = "#5b2a86"
	}
	now := time.Now().Unix()
	anchor := ""

	switch r.PathValue("what") {
	case "settings":
		visible := "0"
		if r.FormValue("visible") != "" {
			visible = "1"
		}
		a.setSetting("election_visible", visible)
		a.setSetting("election_title", short(r.FormValue("title"), 120))

	case "candidate":
		anchor = "#candidates"
		switch {
		case action == "delete":
			a.db.Exec(`DELETE FROM candidates WHERE id=?`, id) // объявленные за него штаты снимаются каскадом
		case name == "":
		case id == 0:
			a.db.Exec(`INSERT INTO candidates(name,party,color) VALUES(?,?,?)`, name, party, color)
		default:
			a.db.Exec(`UPDATE candidates SET name=?,party=?,color=? WHERE id=?`, name, party, color, id)
		}

	case "call":
		anchor = "#calls"
		state := r.FormValue("state")
		cand, _ := strconv.ParseInt(r.FormValue("candidate"), 10, 64)
		switch {
		case stateName(state) == "":
		case cand == 0:
			a.db.Exec(`DELETE FROM state_calls WHERE state=?`, state)
		default:
			// повторное объявление за того же кандидата не сдвигает время
			a.db.Exec(`INSERT INTO state_calls(state,candidate_id,called_at) VALUES(?,?,?)
				ON CONFLICT(state) DO UPDATE SET candidate_id=excluded.candidate_id, called_at=excluded.called_at
				WHERE candidate_id<>excluded.candidate_id`, state, cand, now)
		}

	case "senate":
		anchor = "#senate"
		state := r.FormValue("state")
		contenders, winner := short(r.FormValue("contenders"), 200), short(r.FormValue("winner"), 80)
		switch {
		case action == "delete":
			a.db.Exec(`DELETE FROM senate_races WHERE id=?`, id)
		case stateName(state) == "":
		case id == 0:
			a.db.Exec(`INSERT INTO senate_races(state,contenders) VALUES(?,?)`, state, contenders)
		case winner == "":
			a.db.Exec(`UPDATE senate_races SET state=?,contenders=?,winner='',party='',color='',called_at=NULL WHERE id=?`, state, contenders, id)
		default:
			a.db.Exec(`UPDATE senate_races SET state=?,contenders=?,winner=?,party=?,color=?,called_at=COALESCE(called_at,?) WHERE id=?`,
				state, contenders, winner, party, color, now, id)
		}

	case "reset":
		a.db.Exec(`DELETE FROM state_calls`)
		a.db.Exec(`UPDATE senate_races SET winner='',party='',color='',called_at=NULL`)

	default:
		a.notFound(w, r)
		return
	}
	http.Redirect(w, r, "/admin/elections"+anchor, http.StatusSeeOther)
}
