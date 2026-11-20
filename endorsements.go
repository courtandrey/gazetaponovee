package main

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Публичный Надзор (эндорсменты и донаты): переводы популярности и денег, которые GM-bot публикует в теме
// «Эндорсменты & Донаты». Выгрузку раз в 20 минут делает tools/endorsements_sync.py через tdl
// и отправляет в служебный API; сайт хранит последний снимок целиком.

const (
	kindPop   = "pop"
	kindMoney = "money"
)

type Transfer struct {
	ID             int64
	At             time.Time
	Kind           string
	From, FromRole string
	To, ToRole     string
	Amount         int
	Note           string
}

func (t Transfer) KindName() string {
	if t.Kind == kindMoney {
		return "Деньги"
	}
	return "Популярность"
}

var (
	reTransferHead = regexp.MustCompile(`^[^\p{L}]*(Популярность|Деньги):\s*(.+?)\s+→\s+(.+),\s*(\d+)\s*$`)
	reParty        = regexp.MustCompile(`^(.+?)\s*\((.*)\)$`)
)

func splitParty(s string) (name, role string) {
	s = strings.TrimSpace(s)
	if m := reParty.FindStringSubmatch(s); m != nil {
		return m[1], strings.TrimSpace(m[2])
	}
	return s, ""
}

// parseTransfer разбирает сообщение бота:
//
//	❤️ Популярность: Имя (роль) → Имя (роль), 2
//	«комментарий»
func parseTransfer(text string) (Transfer, bool) {
	head, note, _ := strings.Cut(strings.TrimSpace(strings.ReplaceAll(text, "\r\n", "\n")), "\n")
	m := reTransferHead.FindStringSubmatch(strings.TrimSpace(head))
	if m == nil {
		return Transfer{}, false
	}
	var t Transfer
	t.Kind = kindPop
	if m[1] == "Деньги" {
		t.Kind = kindMoney
	}
	t.From, t.FromRole = splitParty(m[2])
	t.To, t.ToRole = splitParty(m[3])
	if _, err := fmt.Sscan(m[4], &t.Amount); err != nil || t.From == "" || t.To == "" {
		return Transfer{}, false
	}
	note = strings.TrimSpace(note)
	note = strings.TrimSuffix(strings.TrimPrefix(note, "«"), "»")
	t.Note = short(note, 500)
	return t, true
}

type syncMessage struct {
	ID   int64  `json:"id"`
	Date int64  `json:"date"`
	Text string `json:"text"`
}

// replaceTransfers заменяет все переводы снимком из темы: удалённые там сообщения
// (например, тестовые) исчезают и с сайта.
func (a *App) replaceTransfers(msgs []syncMessage) (saved, skipped int, err error) {
	tx, err := a.db.Begin()
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM transfers`); err != nil {
		return 0, 0, err
	}
	for _, m := range msgs {
		t, ok := parseTransfer(m.Text)
		if !ok {
			skipped++
			continue
		}
		if _, err := tx.Exec(`INSERT OR REPLACE INTO transfers(id,at,kind,src,src_role,dst,dst_role,amount,note) VALUES(?,?,?,?,?,?,?,?,?)`,
			m.ID, m.Date, t.Kind, t.From, t.FromRole, t.To, t.ToRole, t.Amount, t.Note); err != nil {
			return 0, 0, err
		}
		saved++
	}
	if _, err := tx.Exec(`INSERT INTO settings(key,value) VALUES('transfers_synced',?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value`, fmt.Sprint(time.Now().Unix())); err != nil {
		return 0, 0, err
	}
	return saved, skipped, tx.Commit()
}

func (a *App) internalTransfers(w http.ResponseWriter, r *http.Request) {
	var in struct{ Messages []syncMessage }
	if err := json.NewDecoder(io.LimitReader(r.Body, 16<<20)).Decode(&in); err != nil || in.Messages == nil {
		http.Error(w, `ожидается JSON {"messages":[{"id":…,"date":…,"text":"…"}]}`, http.StatusBadRequest)
		return
	}
	saved, skipped, err := a.replaceTransfers(in.Messages)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	fmt.Fprintf(w, "переводов: %d, пропущено сообщений: %d\n", saved, skipped)
}

func (a *App) listTransfers() []Transfer {
	var out []Transfer
	rows, err := a.db.Query(`SELECT id,at,kind,src,src_role,dst,dst_role,amount,note FROM transfers ORDER BY at DESC, id DESC`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	for rows.Next() {
		var t Transfer
		var at int64
		rows.Scan(&t.ID, &at, &t.Kind, &t.From, &t.FromRole, &t.To, &t.ToRole, &t.Amount, &t.Note)
		t.At = time.Unix(at, 0)
		out = append(out, t)
	}
	return out
}

// --- Граф ---

type GraphNode struct {
	Name, Role string
	X, Y, R    float64
	LabelY     float64
	Got, Gave  [2]int // по видам: 0 — популярность, 1 — деньги
	Class      string
}

type GraphEdge struct {
	From, To   string
	Kind       string
	Sum, Count int
	D          string
	Width      float64
	Class      string
}

type GraphView struct {
	W, H     int
	Nodes    []*GraphNode
	Edges    []*GraphEdge
	Kind     string
	Selected *GraphNode
	Log      []Transfer
	Total    int
}

// Href — ссылка на страницу графа с видом kind и участником who.
func (v GraphView) Href(kind, who string) string {
	q := url.Values{}
	if kind != "" {
		q.Set("k", kind)
	}
	if who != "" {
		q.Set("who", who)
	}
	h := "/endorsements"
	if len(q) > 0 {
		h += "?" + q.Encode()
	}
	return h + "#graph"
}

// Link — та же страница с другим участником (пустое имя — все участники).
func (v GraphView) Link(who string) string {
	return v.Href(v.Kind, who)
}

// Tab — та же страница с другим видом переводов.
func (v GraphView) Tab(kind string) string {
	who := ""
	if v.Selected != nil {
		who = v.Selected.Name
	}
	return v.Href(kind, who)
}

func kindIndex(kind string) int {
	if kind == kindMoney {
		return 1
	}
	return 0
}

const (
	edgeLen  = 130.0  // желаемая длина связи, px
	graphMin = 1000.0 // ширина холста, если граф помещается
	gap      = 24.0   // зазор между отдельными группами
)

// labelHalf — половина ширины подписи узла (кегль 13px, около 0,55 em на букву).
func labelHalf(nd *GraphNode) float64 {
	return math.Max(nd.R, float64(len([]rune(nd.Name)))*3.7)
}

// forceLayout раскладывает одну связную группу силовым методом Фрюхтермана — Рейнгольда.
// Начальные точки выводятся из имён, поэтому раскладка воспроизводима. По горизонтали
// узлы отталкиваются сильнее: подписи горизонтальные и длинные.
func forceLayout(nodes []*GraphNode, pairs [][2]int) {
	n := len(nodes)
	x, y := make([]float64, n), make([]float64, n)
	spread := edgeLen * math.Sqrt(float64(n))
	for i, nd := range nodes {
		h := fnv.New64a()
		h.Write([]byte(nd.Name))
		v := h.Sum64()
		ang := float64(v%3600) / 3600 * 2 * math.Pi
		rad := (0.2 + 0.8*float64(v>>32%1000)/1000) * spread / 2
		x[i], y[i] = rad*math.Cos(ang), rad*math.Sin(ang)
	}
	const iters = 500
	k := edgeLen
	dx, dy := make([]float64, n), make([]float64, n)
	for it := 0; it < iters; it++ {
		temp := spread / 4 * (1 - float64(it)/iters)
		for i := range dx {
			dx[i], dy[i] = -0.02*x[i], -0.03*y[i] // лёгкое притяжение к центру делает группу компактной
		}
		for i := 0; i < n; i++ {
			for j := i + 1; j < n; j++ {
				ex, ey := x[i]-x[j], y[i]-y[j]
				d := math.Max(math.Hypot(ex*0.55, ey), 0.01)
				f := k * k / d / d
				dx[i] += ex * f
				dy[i] += ey * f
				dx[j] -= ex * f
				dy[j] -= ey * f
			}
		}
		for _, p := range pairs {
			i, j := p[0], p[1]
			ex, ey := x[i]-x[j], y[i]-y[j]
			f := math.Hypot(ex, ey) / k
			dx[i] -= ex * f
			dy[i] -= ey * f
			dx[j] += ex * f
			dy[j] += ey * f
		}
		for i := range x {
			d := math.Max(math.Hypot(dx[i], dy[i]), 0.01)
			step := math.Min(d, temp)
			x[i] += dx[i] / d * step
			y[i] += dy[i] / d * step
		}
	}
	for i, nd := range nodes {
		nd.X, nd.Y = x[i], y[i]
	}
}

// layout раскладывает каждую связную группу отдельно и укладывает группы рядами,
// крупные первыми. Возвращает размер холста: высота растёт вместе с графом.
func layout(nodes []*GraphNode, pairs [][2]int) (w, h int) {
	if len(nodes) == 0 {
		return int(graphMin), 200
	}
	parent := make([]int, len(nodes))
	for i := range parent {
		parent[i] = i
	}
	var find func(int) int
	find = func(i int) int {
		if parent[i] != i {
			parent[i] = find(parent[i])
		}
		return parent[i]
	}
	for _, p := range pairs {
		parent[find(p[0])] = find(p[1])
	}
	type group struct {
		nodes         []*GraphNode
		pairs         [][2]int
		minX, minY    float64
		width, height float64
	}
	byRoot := map[int]*group{}
	var groups []*group
	local := make([]int, len(nodes))
	for i, nd := range nodes {
		r := find(i)
		g := byRoot[r]
		if g == nil {
			g = &group{}
			byRoot[r] = g
			groups = append(groups, g)
		}
		local[i] = len(g.nodes)
		g.nodes = append(g.nodes, nd)
	}
	for _, p := range pairs {
		g := byRoot[find(p[0])]
		g.pairs = append(g.pairs, [2]int{local[p[0]], local[p[1]]})
	}
	maxW := graphMin
	for _, g := range groups {
		forceLayout(g.nodes, g.pairs)
		minX, maxX, minY, maxY := math.Inf(1), math.Inf(-1), math.Inf(1), math.Inf(-1)
		for _, nd := range g.nodes {
			half := labelHalf(nd)
			minX, maxX = math.Min(minX, nd.X-half), math.Max(maxX, nd.X+half)
			minY, maxY = math.Min(minY, nd.Y-nd.R), math.Max(maxY, nd.Y+nd.R+20)
		}
		g.minX, g.minY, g.width, g.height = minX, minY, maxX-minX, maxY-minY
		maxW = math.Max(maxW, g.width+2*gap)
	}
	sort.SliceStable(groups, func(i, j int) bool { return len(groups[i].nodes) > len(groups[j].nodes) })

	// Ряды: группы слева направо, перенос по ширине холста, каждый ряд по центру.
	y := gap
	for i := 0; i < len(groups); {
		rowW, rowH, j := gap, 0.0, i
		for ; j < len(groups) && (j == i || rowW+groups[j].width+gap <= maxW); j++ {
			rowW += groups[j].width + gap
			rowH = math.Max(rowH, groups[j].height)
		}
		x := (maxW-rowW)/2 + gap
		for _, g := range groups[i:j] {
			ox, oy := x-g.minX, y+(rowH-g.height)/2-g.minY
			for _, nd := range g.nodes {
				nd.X = math.Round((nd.X+ox)*10) / 10
				nd.Y = math.Round((nd.Y+oy)*10) / 10
				nd.LabelY = nd.Y + nd.R + 15
			}
			x += g.width + gap
		}
		y += rowH + gap
		i = j
	}
	return int(math.Ceil(maxW)), int(math.Ceil(y))
}

// edgePath — дуга от края одного кружка до края другого. Изгиб всегда вправо по ходу,
// поэтому встречные переводы A→B и B→A расходятся, а деньги изгибаются сильнее популярности.
func edgePath(a, b *GraphNode, kind string) string {
	ex, ey := b.X-a.X, b.Y-a.Y
	d := math.Hypot(ex, ey)
	if d < 1 {
		return ""
	}
	bend := 0.12
	if kind == kindMoney {
		bend = 0.26
	}
	cx, cy := (a.X+b.X)/2+ey/d*bend*d, (a.Y+b.Y)/2-ex/d*bend*d
	toward := func(px, py, qx, qy, by float64) (float64, float64) {
		l := math.Max(math.Hypot(qx-px, qy-py), 0.01)
		return px + (qx-px)/l*by, py + (qy-py)/l*by
	}
	sx, sy := toward(a.X, a.Y, cx, cy, a.R+2)
	tx, ty := toward(b.X, b.Y, cx, cy, b.R+3)
	return fmt.Sprintf("M%.1f %.1fQ%.1f %.1f %.1f %.1f", sx, sy, cx, cy, tx, ty)
}

func (a *App) graphView(kind, who string) GraphView {
	if kind != kindPop && kind != kindMoney {
		kind = ""
	}
	all := a.listTransfers()
	v := GraphView{Kind: kind}

	// Узлы и раскладка — по всем переводам, чтобы фильтр не двигал картину.
	byName := map[string]*GraphNode{}
	node := func(name, role string) *GraphNode {
		nd := byName[name]
		if nd == nil {
			nd = &GraphNode{Name: name}
			byName[name] = nd
			v.Nodes = append(v.Nodes, nd)
		}
		if role != "" {
			nd.Role = role // идём от старых к новым: остаётся последняя известная роль
		}
		return nd
	}
	type key struct{ from, to, kind string }
	edges := map[key]*GraphEdge{}
	pairSeen := map[[2]string]bool{}
	for i := len(all) - 1; i >= 0; i-- { // от старых к новым, чтобы порядок узлов был устойчивым
		t := all[i]
		src, dst := node(t.From, t.FromRole), node(t.To, t.ToRole)
		src.Gave[kindIndex(t.Kind)] += t.Amount
		dst.Got[kindIndex(t.Kind)] += t.Amount
		k := key{t.From, t.To, t.Kind}
		e := edges[k]
		if e == nil {
			e = &GraphEdge{From: t.From, To: t.To, Kind: t.Kind}
			edges[k] = e
			v.Edges = append(v.Edges, e)
		}
		e.Sum += t.Amount
		e.Count++
		p := [2]string{t.From, t.To}
		if p[0] > p[1] {
			p[0], p[1] = p[1], p[0]
		}
		pairSeen[p] = true
	}
	for _, nd := range v.Nodes {
		got := nd.Got[0] + nd.Got[1]
		nd.R = math.Min(7+3*math.Sqrt(float64(got)), 28)
	}
	idx := map[string]int{}
	for i, nd := range v.Nodes {
		idx[nd.Name] = i
	}
	var pairs [][2]int
	for p := range pairSeen {
		if p[0] != p[1] {
			pairs = append(pairs, [2]int{idx[p[0]], idx[p[1]]})
		}
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i][0] != pairs[j][0] {
			return pairs[i][0] < pairs[j][0]
		}
		return pairs[i][1] < pairs[j][1]
	})
	v.W, v.H = layout(v.Nodes, pairs)

	v.Selected = byName[who]
	near := map[string]bool{}
	visible := map[string]bool{}
	for _, e := range v.Edges {
		e.D = edgePath(byName[e.From], byName[e.To], e.Kind)
		e.Width = math.Round(math.Min(1.4+1.2*math.Sqrt(float64(e.Sum)), 8)*10) / 10
		e.Class = "edge edge-" + e.Kind
		if kind != "" && e.Kind != kind {
			e.Class += " off"
			continue
		}
		visible[e.From], visible[e.To] = true, true
		if v.Selected != nil {
			if e.From == who || e.To == who {
				near[e.From], near[e.To] = true, true
			} else {
				e.Class += " dim"
			}
		}
	}
	// Приглушённые рёбра рисуем первыми, чтобы рёбра выбранного узла легли поверх.
	sort.SliceStable(v.Edges, func(i, j int) bool {
		return strings.Contains(v.Edges[i].Class, " dim") && !strings.Contains(v.Edges[j].Class, " dim")
	})
	for _, nd := range v.Nodes {
		nd.Class = "node"
		switch {
		case kind != "" && !visible[nd.Name] && nd != v.Selected:
			nd.Class += " off"
		case nd == v.Selected:
			nd.Class += " sel"
		case v.Selected != nil && !near[nd.Name]:
			nd.Class += " dim"
		}
	}

	for _, t := range all {
		if kind != "" && t.Kind != kind {
			continue
		}
		if v.Selected != nil && t.From != who && t.To != who {
			continue
		}
		v.Log = append(v.Log, t)
	}
	v.Total = len(all)
	return v
}

func (a *App) endorsements(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	g := a.graphView(q.Get("k"), q.Get("who"))
	a.render(w, r, http.StatusOK, "endorsements", map[string]any{"G": g})
}
