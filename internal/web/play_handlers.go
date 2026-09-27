package web

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/fwump38/hidden-isle-app/internal/campaign"
	"github.com/fwump38/hidden-isle-app/internal/db"
	"github.com/fwump38/hidden-isle-app/internal/gamedata"
	"github.com/fwump38/hidden-isle-app/internal/live"
	"github.com/fwump38/hidden-isle-app/internal/oracle"
)

func (s *Server) registerPlay(mux *http.ServeMux) {
	u := func(h http.HandlerFunc) http.Handler { return s.requireUser(h) }
	mux.Handle("GET /c/{cid}/live", u(s.liveStream))
	mux.Handle("GET /c/{cid}/play", u(s.playPage))
	mux.Handle("POST /c/{cid}/play/harm", u(s.playHarm))
	mux.Handle("POST /c/{cid}/play/advance", u(s.playAdvance))
	mux.Handle("POST /c/{cid}/handouts", u(s.pushHandout))
	mux.Handle("POST /c/{cid}/table-key", u(s.rotateTableKey))
	// The TV view: a secret link, no sign-in, public information only.
	mux.HandleFunc("GET /table/{cid}", s.tablePage)
	mux.HandleFunc("GET /table/{cid}/state", s.tableState)
	mux.HandleFunc("GET /table/{cid}/live", s.tableStream)
}

// PublishEvent turns a recorded change into a live "changed" message.
func PublishEvent(h *live.Hub, ev db.Event) {
	if ev.CampaignID == 0 {
		return
	}
	m := live.Message{Type: "changed", Data: map[string]any{"entity": ev.EntityType, "id": ev.EntityID, "actor": ev.ActorID},
		SeerOnly: ev.Visibility == db.VisSeer}
	if ev.Visibility == db.VisOwner && ev.OwnerID != nil {
		m.To = *ev.OwnerID
	}
	h.Publish(ev.CampaignID, m)
}

func (s *Server) liveStream(w http.ResponseWriter, r *http.Request) {
	a := s.actor(r)
	c, err := s.Svc.Campaign(a, pathID(r, "cid"))
	if err != nil || s.Live == nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	s.Live.Serve(w, r, c.ID, a.User.ID, a.IsSeer())
}

// ---------------------------------------------------------------- handouts

type handoutMsg struct {
	ID       uint   `json:"id"`
	Title    string `json:"title"`
	Body     string `json:"body"`
	Card     string `json:"card,omitempty"`
	Meaning  string `json:"meaning,omitempty"`
	ImageURL string `json:"image_url,omitempty"`
}

func (s *Server) handoutMessage(h db.Handout) handoutMsg {
	m := handoutMsg{ID: h.ID, Title: h.Title, Body: h.Body, Card: h.Card, ImageURL: h.ImageURL}
	if h.Card != "" {
		if snap := s.Data.Current(); snap != nil {
			if v, err := oracle.FindVision(snap, h.Card); err == nil {
				m.Meaning = v.Meaning
			}
		}
	}
	return m
}

func (s *Server) pushHandout(w http.ResponseWriter, r *http.Request) {
	cid := pathID(r, "cid")
	h := &db.Handout{CampaignID: cid, Title: r.FormValue("title"), Body: r.FormValue("body"), Card: r.FormValue("card"),
		ImageURL: strings.TrimSpace(r.FormValue("image_url")), OnTable: r.FormValue("on_table") == "on"}
	if n, err := strconv.ParseUint(r.FormValue("to"), 10, 64); err == nil && n > 0 {
		v := uint(n)
		h.ToUserID = &v
	}
	err := s.Svc.PushHandout(s.actor(r), h)
	if err == nil && s.Live != nil {
		m := live.Message{Type: "handout", Data: s.handoutMessage(*h)}
		if h.ToUserID != nil {
			m.To = *h.ToUserID
		}
		s.Live.Publish(cid, m)
	}
	s.done(w, r, err, fmt.Sprintf("/c/%d/play", cid))
}

// ---------------------------------------------------------------- Seer dashboard

type playData struct {
	Agents      []playAgent
	Clocks      []db.Clock
	Adversaries []db.Adversary
	Players     []db.User
	Handouts    []db.Handout
	Events      []db.Event
	Vision      []string
	TableURL    string
	Watching    int
}

type playAgent struct {
	db.Agent
	Player string
}

func (s *Server) playPage(w http.ResponseWriter, r *http.Request) {
	a := s.actor(r)
	if !a.IsSeer() {
		s.fail(w, r, campaign.ErrForbidden)
		return
	}
	c, err := s.Svc.Campaign(a, pathID(r, "cid"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	var d playData
	agents, _ := s.Svc.Agents(a, c.ID)
	names := map[uint]string{}
	d.Players, _ = s.Svc.Members(a, c.ID)
	for _, p := range d.Players {
		names[p.ID] = p.Name
	}
	for _, ag := range agents {
		if ag.Status != "Active" && ag.Status != "Resting" && ag.Status != "Captured" {
			continue
		}
		pa := playAgent{Agent: ag}
		if ag.OwnerID != nil {
			pa.Player = names[*ag.OwnerID]
		}
		d.Agents = append(d.Agents, pa)
	}
	var clocks []db.Clock
	_ = s.Svc.List(a, "clock", c.ID, &clocks, "visibility, name")
	for _, cl := range clocks {
		if cl.Status == "Running" {
			d.Clocks = append(d.Clocks, cl)
		}
	}
	_ = s.Svc.List(a, "adversary", c.ID, &d.Adversaries, "hidden, major desc, name")
	d.Handouts, _ = s.Svc.Handouts(a, c.ID, 5)
	d.Events, _ = s.Svc.Events(a, c.ID, campaign.EventFilter{Limit: 10})
	if snap := s.Data.Current(); snap != nil {
		for _, v := range snap.Cards.Vision {
			d.Vision = append(d.Vision, v.Name)
		}
	}
	if key, err := s.Svc.TableKey(a, c.ID, false); err == nil {
		d.TableURL = fmt.Sprintf("/table/%d?key=%s", c.ID, key)
	}
	if s.Live != nil {
		d.Watching = s.Live.Subscribers(c.ID)
	}
	s.page(w, r, "play", "Play", c, "play", d)
}

func (s *Server) playHarm(w http.ResponseWriter, r *http.Request) {
	cid := pathID(r, "cid")
	a := s.actor(r)
	id, _ := strconv.ParseUint(r.FormValue("agent_id"), 10, 64)
	o := campaign.Opts{Reason: strings.TrimSpace(r.FormValue("why"))}
	var err error
	switch r.FormValue("op") {
	case "harm":
		n, _ := strconv.Atoi(orDefault(r.FormValue("amount"), "1"))
		_, err = s.Svc.AddHarm(a, uint(id), r.FormValue("suit"), r.FormValue("type"), n, o)
	case "heal":
		_, _, err = s.Svc.Heal(a, uint(id), r.FormValue("type"), 1, r.FormValue("suit"), o)
	default:
		err = fmt.Errorf("unknown operation")
	}
	s.done(w, r, err, fmt.Sprintf("/c/%d/play", cid))
}

func (s *Server) playAdvance(w http.ResponseWriter, r *http.Request) {
	cid := pathID(r, "cid")
	id, _ := strconv.ParseUint(r.FormValue("adversary_id"), 10, 64)
	_, _, err := s.Svc.AdvanceAdversary(s.actor(r), uint(id), r.FormValue("step"), campaign.Opts{Reason: strings.TrimSpace(r.FormValue("why"))})
	s.done(w, r, err, fmt.Sprintf("/c/%d/play", cid))
}

func (s *Server) rotateTableKey(w http.ResponseWriter, r *http.Request) {
	cid := pathID(r, "cid")
	_, err := s.Svc.TableKey(s.actor(r), cid, true)
	s.done(w, r, err, fmt.Sprintf("/c/%d/play", cid))
}

// ---------------------------------------------------------------- the TV view

type tableData struct {
	State   *campaign.PublicState
	Key     string
	Snap    *gamedata.Snapshot
	Handout *handoutMsg
}

func (s *Server) tableData(r *http.Request) (*tableData, bool) {
	cid, key := pathID(r, "cid"), r.URL.Query().Get("key")
	if !s.Svc.CheckTableKey(cid, key) {
		return nil, false
	}
	st, err := s.Svc.PublicState(cid)
	if err != nil {
		return nil, false
	}
	d := &tableData{State: st, Key: key, Snap: s.Data.Current()}
	if st.Handout != nil {
		m := s.handoutMessage(*st.Handout)
		d.Handout = &m
	}
	return d, true
}

func (s *Server) tablePage(w http.ResponseWriter, r *http.Request) {
	d, ok := s.tableData(r)
	if !ok {
		http.Error(w, "This TV link isn't valid (the Seer may have made a new one).", http.StatusNotFound)
		return
	}
	w.Header().Set("Referrer-Policy", "no-referrer") // keep the key out of Referer headers
	s.partial(w, "table", "tv", d)
}

func (s *Server) tableState(w http.ResponseWriter, r *http.Request) {
	d, ok := s.tableData(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	s.partial(w, "table", "tv-state", d)
}

func (s *Server) tableStream(w http.ResponseWriter, r *http.Request) {
	if !s.Svc.CheckTableKey(pathID(r, "cid"), r.URL.Query().Get("key")) || s.Live == nil {
		http.NotFound(w, r)
		return
	}
	s.Live.Serve(w, r, pathID(r, "cid"), 0, false)
}
