package web

import (
	"net/http"

	"github.com/fwump38/hidden-isle-app/internal/cards"
	"github.com/fwump38/hidden-isle-app/internal/challenge"
	"github.com/fwump38/hidden-isle-app/internal/gamedata"
	"github.com/fwump38/hidden-isle-app/internal/oracle"
)

type oraclePage struct {
	Pips    []string
	Vision  []string
	Regions []string
	Empty   *oracleOut // renders the empty result area
}

// oracleOut is one reading, rendered into #oracle-out.
type oracleOut struct {
	Title   string
	Drawn   []string
	Fate    *oracle.FateResult
	Event   *oracle.Event
	NPC     *oracle.NPC
	Comp    *oracle.Complication
	Mission *oracle.Mission
	Vision  *gamedata.VisionCard
	Error   string
}

func (s *Server) oraclePage(w http.ResponseWriter, r *http.Request) {
	a := s.actor(r)
	c, err := s.Svc.Campaign(a, pathID(r, "cid"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	d := oraclePage{Pips: s.pipNames()}
	if snap := s.Data.Current(); snap != nil {
		for _, v := range snap.Cards.Vision {
			d.Vision = append(d.Vision, v.Name)
		}
		if t, err := oracle.Load(snap); err == nil {
			d.Regions = t.Regions()
		}
	}
	s.page(w, r, "oracle", "Oracle", c, "oracle", d)
}

// oracleAction handles every oracle form; action=draw fills the needed cards digitally first.
func (s *Server) oracleAction(w http.ResponseWriter, r *http.Request) {
	a := s.actor(r)
	var out oracleOut
	defer func() { s.partial(w, "oracle", "oracle-out", out) }()
	if _, err := s.Svc.Campaign(a, pathID(r, "cid")); err != nil {
		out.Error = friendly(err)
		return
	}
	snap := s.Data.Current()
	t, err := oracle.Load(snap)
	if err != nil {
		out.Error = err.Error()
		return
	}
	_ = r.ParseForm()
	f := r.PostForm
	draw := f.Get("action") == "draw"

	// digital fills in empty card fields from one shuffled deck of the right kind.
	digital := func(deck string, fields ...string) bool {
		if !draw {
			return true
		}
		hands, err := cards.Draw(snap, deck, []cards.Request{{Count: len(fields)}})
		if err != nil {
			out.Error = err.Error()
			return false
		}
		for i, fld := range fields {
			f.Set(fld, hands[0].Cards[i])
			out.Drawn = append(out.Drawn, hands[0].Cards[i])
		}
		return true
	}
	pip := func(field string) (challenge.Card, bool) {
		c, err := challenge.ParseCard(f.Get(field))
		if err != nil {
			out.Error = "Choose the card drawn (or press Draw for a digital draw)."
			return c, false
		}
		return c, true
	}
	vision := func(field string) (gamedata.VisionCard, bool) {
		v, err := oracle.FindVision(snap, f.Get(field))
		if err != nil {
			out.Error = "Choose the vision card drawn (or press Draw for a digital draw)."
			return v, false
		}
		return v, true
	}

	switch f.Get("tool") {
	case "closed":
		out.Title = "Fate question (yes or no)"
		ny, nn, err := t.Hands(f.Get("likelihood"))
		if err != nil {
			out.Error = err.Error()
			return
		}
		var fields []string
		for i := 0; i < ny; i++ {
			fields = append(fields, "yes"+string(rune('0'+i)))
		}
		for i := 0; i < nn; i++ {
			fields = append(fields, "no"+string(rune('0'+i)))
		}
		if !digital("pips", fields...) {
			return
		}
		var yes, no []challenge.Card
		for i := 0; i < 2; i++ {
			if c, err := challenge.ParseCard(f.Get("yes" + string(rune('0'+i)))); err == nil {
				yes = append(yes, c)
			}
			if c, err := challenge.ParseCard(f.Get("no" + string(rune('0'+i)))); err == nil {
				no = append(no, c)
			}
		}
		res, err := t.Closed(f.Get("likelihood"), yes, no, f.Get("ace") != "low")
		if err != nil {
			out.Error = err.Error()
			return
		}
		out.Fate = &res
	case "numeric":
		out.Title = "Fate question (a number)"
		if !digital("pips", "card") {
			return
		}
		c, ok := pip("card")
		if !ok {
			return
		}
		res, err := oracle.Numeric(f.Get("range"), c)
		if err != nil {
			out.Error = err.Error()
			return
		}
		out.Fate = &res
	case "open":
		out.Title = "Open question"
		if !digital("vision", "vision") {
			return
		}
		v, ok := vision("vision")
		if !ok {
			return
		}
		out.Vision = &v
	case "event":
		out.Title = "Random event"
		if !digital("pips", "card") {
			return
		}
		c, ok := pip("card")
		if !ok {
			return
		}
		e := t.RandomEvent(c)
		out.Event = &e
	case "npc":
		out.Title = "NPC"
		if draw {
			if !digital("vision", "vision") {
				return
			}
			hands, _ := cards.Draw(snap, "pips", []cards.Request{{Count: 1}})
			f.Set("method", hands[0].Cards[0])
			out.Drawn = append(out.Drawn, hands[0].Cards[0])
		}
		v, ok := vision("vision")
		if !ok {
			return
		}
		var method *challenge.Card
		if m, err := challenge.ParseCard(f.Get("method")); err == nil {
			method = &m
		}
		n := t.MakeNPC(v, method, f.Get("region"))
		out.NPC = &n
	case "complication":
		out.Title = "Complication ideas"
		if !digital("vision", "vision") {
			return
		}
		v, ok := vision("vision")
		if !ok {
			return
		}
		cm := oracle.MakeComplication(v)
		out.Comp = &cm
	case "mission":
		out.Title = "Mission type"
		if !digital("pips", "card") {
			return
		}
		c, ok := pip("card")
		if !ok {
			return
		}
		m, err := t.MissionType(c)
		if err != nil {
			out.Error = err.Error()
			return
		}
		out.Mission = &m
	default:
		out.Error = "unknown oracle tool"
	}
}
