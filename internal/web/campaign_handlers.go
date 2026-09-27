package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/fwump38/hidden-isle-app/internal/auth"
	"github.com/fwump38/hidden-isle-app/internal/campaign"
	"github.com/fwump38/hidden-isle-app/internal/db"
	"github.com/fwump38/hidden-isle-app/internal/gamedata"
)

func (s *Server) registerCampaign(mux *http.ServeMux) {
	u := func(h http.HandlerFunc) http.Handler { return s.requireUser(h) }
	mux.Handle("POST /campaigns", u(s.createCampaign))
	mux.Handle("GET /c/{cid}", u(s.campaignPage))
	mux.Handle("GET /c/{cid}/settings", u(s.settingsPage))
	mux.Handle("POST /c/{cid}/members", u(s.setMember))
	mux.Handle("POST /c/{cid}/agents", u(s.createAgent))
	mux.Handle("GET /c/{cid}/log", u(s.logPage))
	mux.Handle("GET /c/{cid}/journal", u(s.journalPage))
	mux.Handle("POST /c/{cid}/entries", u(s.saveEntry))
	mux.Handle("POST /entries/{id}/delete", u(s.deleteEntry))
	mux.Handle("GET /c/{cid}/sessions/{id}", u(s.sessionPage))
	mux.Handle("GET /c/{cid}/export.json", u(s.exportJSON))
	mux.Handle("GET /c/{cid}/export.md", u(s.exportMarkdown))
	mux.Handle("GET /c/{cid}/{section}", u(s.recordsPage))
	mux.Handle("POST /c/{cid}/r/{kind}", u(s.createRecord))
	mux.Handle("POST /r/{kind}/{id}", u(s.updateRecord))
	mux.Handle("POST /r/{kind}/{id}/delete", u(s.deleteRecord))
	mux.Handle("POST /events/{id}/undo", u(s.undoEvent))

	mux.Handle("POST /agents", u(s.createAgent))
	mux.Handle("POST /agents/{id}/campaign", u(s.assignAgent))
	mux.Handle("POST /c/{cid}/bring", u(s.bringAgent))
	mux.Handle("POST /agents/{id}/delete", u(s.deleteAgent))
	mux.Handle("POST /c/{cid}/delete", u(s.deleteCampaign))
	mux.Handle("GET /agents/{id}", u(s.agentPage))
	mux.Handle("GET /agents/{id}/print", u(s.agentPrint))
	mux.Handle("POST /agents/{id}", u(s.updateAgent))
	mux.Handle("POST /agents/{id}/contacts", u(s.createContact))
	mux.Handle("POST /agents/{id}/challenge", u(s.challengeHelper))
}

func (s *Server) actor(r *http.Request) campaign.Actor {
	return campaign.Actor{User: auth.User(r.Context()), Via: "web"}
}

func pathID(r *http.Request, name string) uint {
	n, _ := strconv.ParseUint(r.PathValue(name), 10, 64)
	return uint(n)
}

// ---------------------------------------------------------------- flash + redirects

const flashCookie = "hi_flash"

// done redirects back after a POST, carrying err (if any) as a one-time message.
func (s *Server) done(w http.ResponseWriter, r *http.Request, err error, fallback string) {
	back := r.FormValue("back")
	if !strings.HasPrefix(back, "/") || strings.HasPrefix(back, "//") {
		back = fallback
	}
	if err != nil {
		http.SetCookie(w, &http.Cookie{Name: flashCookie, Value: url.QueryEscape(friendly(err)), Path: "/", MaxAge: 60, HttpOnly: true, SameSite: http.SameSiteLaxMode})
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// takeFlash reads and clears the one-time message.
func takeFlash(w http.ResponseWriter, r *http.Request) string {
	c, err := r.Cookie(flashCookie)
	if err != nil {
		return ""
	}
	http.SetCookie(w, &http.Cookie{Name: flashCookie, Value: "", Path: "/", MaxAge: -1})
	msg, _ := url.QueryUnescape(c.Value)
	return msg
}

func friendly(err error) string {
	var ve *campaign.ValidationError
	switch {
	case errors.As(err, &ve):
		return err.Error()
	case campaign.IsForbidden(err):
		return "You can't change that."
	case campaign.IsNotFound(err):
		return "That doesn't exist (any more)."
	}
	return err.Error()
}

// fail renders an error page for GET handlers.
func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error) {
	status := http.StatusInternalServerError
	switch {
	case campaign.IsForbidden(err):
		status = http.StatusForbidden
	case campaign.IsNotFound(err):
		status = http.StatusNotFound
	}
	s.render(w, r, "denied", status, pageData{Title: http.StatusText(status), Error: friendly(err)})
}

// ---------------------------------------------------------------- page data

// campaignNav is what the campaign pages share.
type campaignNav struct {
	Campaign *db.Campaign
	Section  string
	Snap     *gamedata.Snapshot
}

// page renders a page; c may be nil (an Agent outside any campaign).
func (s *Server) page(w http.ResponseWriter, r *http.Request, name, title string, c *db.Campaign, section string, data any) {
	s.render(w, r, name, http.StatusOK, pageData{Title: title, Error: takeFlash(w, r), Data: data,
		Nav: &campaignNav{Campaign: c, Section: section, Snap: s.Data.Current()}})
}

// ---------------------------------------------------------------- campaigns

func (s *Server) createCampaign(w http.ResponseWriter, r *http.Request) {
	c := &db.Campaign{Name: strings.TrimSpace(r.FormValue("name")), Mode: r.FormValue("mode"), Merciful: r.FormValue("merciful") == "on"}
	err := s.Svc.CreateCampaign(s.actor(r), c, campaign.Opts{Reason: "new campaign"})
	if err != nil {
		s.done(w, r, err, "/")
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/c/%d", c.ID), http.StatusSeeOther)
}

type dashboardData struct {
	AllPlayers  []db.User // Seer: every active player (for "add a player" and "played by")
	Members     map[uint]bool
	MyOther     []db.Agent // the player's Agents not in this campaign
	Agents      []db.Agent
	Clocks      []db.Clock
	Adversaries []db.Adversary
	Sessions    []db.Session
	Events      []db.Event
	Recaps      []db.Entry
	Players     []db.User
	MyAgents    int
}

func (s *Server) campaignPage(w http.ResponseWriter, r *http.Request) {
	a := s.actor(r)
	c, err := s.Svc.Campaign(a, pathID(r, "cid"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	var d dashboardData
	d.Agents, _ = s.Svc.Agents(a, c.ID)
	for _, ag := range d.Agents {
		if ag.OwnerID != nil && *ag.OwnerID == a.User.ID {
			d.MyAgents++
		}
	}
	_ = s.Svc.List(a, "clock", c.ID, &d.Clocks, "status, name")
	_ = s.Svc.List(a, "adversary", c.ID, &d.Adversaries, "major desc, name")
	_ = s.Svc.List(a, "session", c.ID, &d.Sessions, "number desc")
	d.Events, _ = s.Svc.Events(a, c.ID, campaign.EventFilter{Limit: 12})
	d.Recaps, _ = s.Svc.Entries(a, c.ID, "recap", 0)
	d.Players, _ = s.Svc.Members(a, c.ID)
	d.Members = map[uint]bool{}
	for _, m := range d.Players {
		d.Members[m.ID] = true
	}
	if a.IsSeer() {
		s.DB.Where("role = ? AND active = ?", db.RolePlayer, true).Order("name").Find(&d.AllPlayers)
	}
	mine, _ := s.Svc.MyAgents(a)
	for _, ag := range mine {
		if ag.CampaignID != c.ID {
			d.MyOther = append(d.MyOther, ag)
		}
	}
	s.page(w, r, "campaign", c.Name, c, "overview", d)
}

type settingsData struct {
	Users   []db.User
	Members map[uint]bool
}

func (s *Server) settingsPage(w http.ResponseWriter, r *http.Request) {
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
	d := settingsData{Members: map[uint]bool{}}
	s.DB.Where("role = ? AND active = ?", db.RolePlayer, true).Order("name").Find(&d.Users)
	members, _ := s.Svc.Members(a, c.ID)
	for _, m := range members {
		d.Members[m.ID] = true
	}
	s.page(w, r, "settings", "Settings", c, "settings", d)
}

func (s *Server) setMember(w http.ResponseWriter, r *http.Request) {
	cid := pathID(r, "cid")
	uid, _ := strconv.ParseUint(r.FormValue("user_id"), 10, 64)
	err := s.Svc.SetMember(s.actor(r), cid, uint(uid), r.FormValue("member") == "on")
	s.done(w, r, err, fmt.Sprintf("/c/%d/settings", cid))
}

// ---------------------------------------------------------------- agents

func (s *Server) createAgent(w http.ResponseWriter, r *http.Request) {
	cid := pathID(r, "cid")
	if cid == 0 {
		if n, err := strconv.ParseUint(r.FormValue("campaign_id"), 10, 64); err == nil {
			cid = uint(n)
		}
	}
	var owner *uint
	if n, err := strconv.ParseUint(r.FormValue("owner_id"), 10, 64); err == nil && n > 0 {
		o := uint(n)
		owner = &o
	}
	back := "/"
	if cid != 0 {
		back = fmt.Sprintf("/c/%d", cid)
	}
	ag, err := s.Svc.NewAgent(s.actor(r), cid, strings.TrimSpace(r.FormValue("name")), r.FormValue("class"), owner, campaign.Opts{})
	if err != nil {
		s.done(w, r, err, back)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/agents/%d", ag.ID), http.StatusSeeOther)
}

func (s *Server) assignAgent(w http.ResponseWriter, r *http.Request) {
	id := pathID(r, "id")
	cid, _ := strconv.ParseUint(r.FormValue("campaign_id"), 10, 64)
	err := s.Svc.AssignAgent(s.actor(r), id, uint(cid), campaign.Opts{Reason: strings.TrimSpace(r.FormValue("why"))})
	s.done(w, r, err, fmt.Sprintf("/agents/%d", id))
}

// bringAgent moves one of the player's Agents into this campaign.
func (s *Server) bringAgent(w http.ResponseWriter, r *http.Request) {
	cid := pathID(r, "cid")
	id, _ := strconv.ParseUint(r.FormValue("agent_id"), 10, 64)
	err := s.Svc.AssignAgent(s.actor(r), uint(id), cid, campaign.Opts{})
	s.done(w, r, err, fmt.Sprintf("/c/%d", cid))
}

func (s *Server) deleteAgent(w http.ResponseWriter, r *http.Request) {
	id := pathID(r, "id")
	a := s.actor(r)
	ag, err := s.Svc.Agent(a, id)
	back := "/"
	if err == nil {
		if ag.CampaignID != 0 {
			back = fmt.Sprintf("/c/%d", ag.CampaignID)
		}
		err = s.Svc.Delete(a, "agent", id, campaign.Opts{Reason: strings.TrimSpace(r.FormValue("why"))})
	}
	if err != nil {
		s.done(w, r, err, fmt.Sprintf("/agents/%d", id))
		return
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

func (s *Server) deleteCampaign(w http.ResponseWriter, r *http.Request) {
	cid := pathID(r, "cid")
	if err := s.Svc.DeleteCampaign(s.actor(r), cid, r.FormValue("confirm")); err != nil {
		s.done(w, r, err, fmt.Sprintf("/c/%d/settings", cid))
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

type sheetData struct {
	Campaign  *db.Campaign  // nil when the Agent isn't in a campaign
	Campaigns []db.Campaign // where the owner may move it
	Agent     *db.Agent
	Class     *gamedata.Class
	CanEdit   bool
	Owner     string
	Contacts  []db.Contact
	Clocks    []db.Clock
	Events    []db.Event
	History   []db.Entry
	Players   []db.User
	Suits     []suitRow
	Items     []gamedata.Item // common + class items, for the pull list
	Abil      []abilityView   // the Agent's abilities with their text
	Unused    []gamedata.Ability
	Schools   []string
	Pips      []string // the 40 pips, for card pickers
}

type suitRow struct {
	Suit   string
	Skills []skillCell
	XP     int
	XPKey  string
	Harm   [2]string
}

type skillCell struct {
	Name     string
	Points   int
	Unlocked bool
}

type abilityView struct {
	Index                 int
	Name, Text, Sheet     string
	Page                  int
	Custom                bool
	Source, ClassOfOrigin string
}

func (s *Server) agentSheet(a campaign.Actor, id uint) (*sheetData, *db.Campaign, error) {
	ag, err := s.Svc.Agent(a, id)
	if err != nil {
		return nil, nil, err
	}
	var c *db.Campaign
	if ag.CampaignID != 0 {
		if c, err = s.Svc.Campaign(a, ag.CampaignID); err != nil && !a.IsSeer() {
			// The owner left the campaign's membership but the Agent is still there.
			c = nil
		}
	}
	snap := s.Data.Current()
	if snap == nil {
		return nil, nil, campaign.ErrNoData
	}
	d := &sheetData{Campaign: c, Agent: ag, Class: snap.Class(ag.Class), CanEdit: s.Svc.CanEditAgent(a, ag), Pips: s.pipNames()}
	if ag.OwnerID != nil {
		var u db.User
		if s.DB.First(&u, *ag.OwnerID).Error == nil {
			d.Owner = u.Name
		}
	}
	if d.CanEdit {
		d.Campaigns, _ = s.Svc.Campaigns(a)
	}
	d.Contacts, _ = s.Svc.Contacts(a, ag.ID)
	if c != nil {
		var clocks []db.Clock
		_ = s.Svc.List(a, "clock", c.ID, &clocks, "name")
		for _, cl := range clocks {
			if cl.AgentID != nil && *cl.AgentID == ag.ID {
				d.Clocks = append(d.Clocks, cl)
			}
		}
	}
	d.Events, _ = s.Svc.AgentEvents(a, ag.ID, 25)
	d.History, _ = s.Svc.AgentHistory(a, ag.ID)
	if a.IsSeer() {
		s.DB.Where("role = ? AND active = ?", db.RolePlayer, true).Order("name").Find(&d.Players)
	}
	xp := map[string]struct {
		v   int
		key string
	}{"Swords": {ag.XPSwords, "xp_swords"}, "Wands": {ag.XPWands, "xp_wands"}, "Cups": {ag.XPCups, "xp_cups"}, "Pentacles": {ag.XPPentacles, "xp_pentacles"}}
	for _, suit := range []string{"Swords", "Wands", "Cups", "Pentacles"} {
		row := suitRow{Suit: suit, XP: xp[suit].v, XPKey: xp[suit].key}
		for _, sk := range snap.Skills.Skills {
			if sk.Suit == suit {
				row.Skills = append(row.Skills, skillCell{Name: sk.Name, Points: ag.Skills[sk.Name], Unlocked: slices.Contains(ag.UnlockedFourth, sk.Name)})
			}
		}
		for i, m := range ag.Harm[suit] {
			if i < 2 {
				row.Harm[i] = m
			}
		}
		d.Suits = append(d.Suits, row)
	}
	d.Items = append(d.Items, snap.Classes.CommonItems...)
	if d.Class != nil {
		d.Items = append(d.Items, d.Class.Items...)
	}
	have := map[string]bool{}
	for i, ab := range ag.Abilities {
		v := abilityView{Index: i, Name: ab.Name, Text: ab.Text, Source: ab.Source, Custom: ab.ID == ""}
		if ab.ID != "" {
			have[ab.ID] = true
			for _, cl := range snap.Classes.Classes {
				for _, x := range cl.Abilities {
					if x.ID == ab.ID {
						v.Name, v.Text, v.Sheet, v.Page = x.Name, x.Text, x.SheetText, x.Page
						if cl.ID != ag.Class {
							v.ClassOfOrigin = cl.Name
						}
					}
				}
			}
		}
		d.Abil = append(d.Abil, v)
	}
	if set, ok := snap.Raw["setting"]; ok {
		if prof, ok := set["proficiencies"].(map[string]any); ok {
			if schools, ok := prof["schools"].([]any); ok {
				for _, sc := range schools {
					if m, ok := sc.(map[string]any); ok {
						d.Schools = append(d.Schools, fmt.Sprint(m["name"]))
					}
				}
			}
		}
	}
	if d.Class != nil {
		for _, x := range d.Class.Abilities {
			if !have[x.ID] {
				d.Unused = append(d.Unused, x)
			}
		}
	}
	return d, c, nil
}

func (s *Server) agentPage(w http.ResponseWriter, r *http.Request) {
	d, c, err := s.agentSheet(s.actor(r), pathID(r, "id"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.page(w, r, "agent", d.Agent.Name, c, "agents", d)
}

func (s *Server) agentPrint(w http.ResponseWriter, r *http.Request) {
	d, c, err := s.agentSheet(s.actor(r), pathID(r, "id"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.page(w, r, "print", d.Agent.Name, c, "agents", d)
}

func (s *Server) updateAgent(w http.ResponseWriter, r *http.Request) {
	id := pathID(r, "id")
	back := fmt.Sprintf("/agents/%d", id)
	a := s.actor(r)
	ag, err := s.Svc.Agent(a, id)
	if err != nil {
		s.done(w, r, err, back)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.done(w, r, err, back)
		return
	}
	p, err := agentPatch(ag, r.PostForm)
	if err == nil && len(p) > 0 {
		_, err = s.Svc.Update(a, "agent", id, p, writeOpts(r.PostForm))
	}
	s.done(w, r, err, back)
}

func (s *Server) createContact(w http.ResponseWriter, r *http.Request) {
	id := pathID(r, "id")
	a := s.actor(r)
	ag, err := s.Svc.Agent(a, id)
	if err == nil {
		c := &db.Contact{CampaignID: ag.CampaignID, AgentID: ag.ID}
		err = s.createFromForm(a, "contact", c, r)
	}
	s.done(w, r, err, fmt.Sprintf("/agents/%d", id))
}

// ---------------------------------------------------------------- generic records

// sections maps a campaign page to its record type and sort order.
var sections = map[string]struct{ kind, title, order string }{
	"clocks":      {"clock", "Clocks", "status, name"},
	"adversaries": {"adversary", "Adversaries", "major desc, hidden, name"},
	"territories": {"territory", "Territories", "id"},
	"sessions":    {"session", "Sessions", "number desc"},
	"notes":       {"seer_note", "Seer notes", "title"},
	"rulings":     {"house_ruling", "House rulings", "created_at desc"},
}

type recordsData struct {
	Kind    string
	Records any
	Agents  []db.Agent
}

func (s *Server) recordsPage(w http.ResponseWriter, r *http.Request) {
	a := s.actor(r)
	sec, ok := sections[r.PathValue("section")]
	if !ok {
		http.NotFound(w, r)
		return
	}
	c, err := s.Svc.Campaign(a, pathID(r, "cid"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	var list any
	switch sec.kind {
	case "clock":
		list = &[]db.Clock{}
	case "adversary":
		list = &[]db.Adversary{}
	case "territory":
		list = &[]db.Territory{}
	case "session":
		list = &[]db.Session{}
	case "seer_note":
		list = &[]db.SeerNote{}
	case "house_ruling":
		list = &[]db.HouseRuling{}
	}
	if err := s.Svc.List(a, sec.kind, c.ID, list, sec.order); err != nil {
		s.fail(w, r, err)
		return
	}
	d := recordsData{Kind: sec.kind, Records: list}
	d.Agents, _ = s.Svc.Agents(a, c.ID)
	s.page(w, r, "records", sec.title, c, r.PathValue("section"), d)
}

func newRecord(kind string, cid uint) (any, bool) {
	switch kind {
	case "clock":
		return &db.Clock{CampaignID: cid, Status: "Running", Visibility: db.VisParty}, true
	case "adversary":
		return &db.Adversary{CampaignID: cid, Status: "Rumored"}, true
	case "territory":
		return &db.Territory{CampaignID: cid}, true
	case "session":
		return &db.Session{CampaignID: cid, Status: "Prep"}, true
	case "seer_note":
		return &db.SeerNote{CampaignID: cid}, true
	case "house_ruling":
		return &db.HouseRuling{CampaignID: cid}, true
	}
	return nil, false
}

// createFromForm fills obj from set.* fields and creates it.
func (s *Server) createFromForm(a campaign.Actor, kind string, obj any, r *http.Request) error {
	if err := r.ParseForm(); err != nil {
		return err
	}
	p, err := recordPatch(obj, r.PostForm)
	if err != nil {
		return err
	}
	for f := range p {
		if f == "id" || f == "campaign_id" {
			delete(p, f)
		}
	}
	b, _ := json.Marshal(p)
	if err := json.Unmarshal(b, obj); err != nil {
		return err
	}
	return s.Svc.Create(a, kind, obj, writeOpts(r.PostForm))
}

func (s *Server) createRecord(w http.ResponseWriter, r *http.Request) {
	cid := pathID(r, "cid")
	obj, ok := newRecord(r.PathValue("kind"), cid)
	var err error
	if !ok {
		err = fmt.Errorf("can't create a %s here", r.PathValue("kind"))
	} else {
		if sess, isSess := obj.(*db.Session); isSess {
			var n int64
			s.DB.Model(&db.Session{}).Where("campaign_id = ?", cid).Count(&n)
			sess.Number = int(n) + 1
		}
		err = s.createFromForm(s.actor(r), r.PathValue("kind"), obj, r)
	}
	s.done(w, r, err, fmt.Sprintf("/c/%d", cid))
}

func (s *Server) updateRecord(w http.ResponseWriter, r *http.Request) {
	a := s.actor(r)
	kind, id := r.PathValue("kind"), pathID(r, "id")
	obj, err := s.Svc.Get(a, kind, id)
	if err == nil {
		err = r.ParseForm()
	}
	var p campaign.Patch
	if err == nil {
		p, err = recordPatch(obj, r.PostForm)
	}
	if err == nil && len(p) > 0 {
		_, err = s.Svc.Update(a, kind, id, p, writeOpts(r.PostForm))
	}
	s.done(w, r, err, "/")
}

func (s *Server) deleteRecord(w http.ResponseWriter, r *http.Request) {
	err := s.Svc.Delete(s.actor(r), r.PathValue("kind"), pathID(r, "id"), campaign.Opts{Reason: r.FormValue("why")})
	s.done(w, r, err, "/")
}

// ---------------------------------------------------------------- sessions, log, journal

type sessionData struct {
	Session *db.Session
	Logs    []db.Entry
	Events  []db.Event
}

func (s *Server) sessionPage(w http.ResponseWriter, r *http.Request) {
	a := s.actor(r)
	c, err := s.Svc.Campaign(a, pathID(r, "cid"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	obj, err := s.Svc.Get(a, "session", pathID(r, "id"))
	if err != nil || obj.(*db.Session).CampaignID != c.ID {
		s.fail(w, r, campaign.ErrNotFound)
		return
	}
	d := sessionData{Session: obj.(*db.Session)}
	all, _ := s.Svc.Entries(a, c.ID, "", 0)
	for _, e := range all {
		if e.SessionID != nil && *e.SessionID == d.Session.ID {
			d.Logs = append(d.Logs, e)
		}
	}
	d.Events, _ = s.Svc.Events(a, c.ID, campaign.EventFilter{SessionID: d.Session.ID, Limit: 100})
	s.page(w, r, "session", d.Session.Title, c, "sessions", d)
}

type logData struct {
	Events []db.Event
	Next   uint
	Me     uint
}

func (s *Server) logPage(w http.ResponseWriter, r *http.Request) {
	a := s.actor(r)
	c, err := s.Svc.Campaign(a, pathID(r, "cid"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	before, _ := strconv.ParseUint(r.URL.Query().Get("before"), 10, 64)
	evs, err := s.Svc.Events(a, c.ID, campaign.EventFilter{Limit: 50, BeforeID: uint(before), EntityType: r.URL.Query().Get("type")})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	d := logData{Events: evs, Me: a.User.ID}
	if len(evs) == 50 {
		d.Next = evs[len(evs)-1].ID
	}
	s.page(w, r, "log", "Change log", c, "log", d)
}

func (s *Server) undoEvent(w http.ResponseWriter, r *http.Request) {
	err := s.Svc.Revert(s.actor(r), pathID(r, "id"), strings.TrimSpace(r.FormValue("why")))
	s.done(w, r, err, "/")
}

type journalData struct {
	Entries  []db.Entry
	Sessions []db.Session
	Agents   []db.Agent
	Me       uint
}

func (s *Server) journalPage(w http.ResponseWriter, r *http.Request) {
	a := s.actor(r)
	c, err := s.Svc.Campaign(a, pathID(r, "cid"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	d := journalData{Me: a.User.ID}
	d.Entries, _ = s.Svc.Entries(a, c.ID, r.URL.Query().Get("kind"), 0)
	_ = s.Svc.List(a, "session", c.ID, &d.Sessions, "number desc")
	d.Agents, _ = s.Svc.Agents(a, c.ID)
	s.page(w, r, "journal", "Journal", c, "journal", d)
}

func (s *Server) saveEntry(w http.ResponseWriter, r *http.Request) {
	cid := pathID(r, "cid")
	e := &db.Entry{CampaignID: cid, Kind: r.FormValue("kind"), Title: strings.TrimSpace(r.FormValue("title")),
		Body: strings.TrimSpace(r.FormValue("body")), Visibility: db.Visibility(r.FormValue("visibility")), Published: r.FormValue("published") == "on"}
	if id, err := strconv.ParseUint(r.FormValue("id"), 10, 64); err == nil {
		e.ID = uint(id)
	}
	if n, err := strconv.ParseUint(r.FormValue("session_id"), 10, 64); err == nil && n > 0 {
		v := uint(n)
		e.SessionID = &v
	}
	if n, err := strconv.ParseUint(r.FormValue("agent_id"), 10, 64); err == nil && n > 0 {
		v := uint(n)
		e.AgentID = &v
	}
	s.done(w, r, s.Svc.WriteEntry(s.actor(r), e), fmt.Sprintf("/c/%d/journal", cid))
}

func (s *Server) deleteEntry(w http.ResponseWriter, r *http.Request) {
	s.done(w, r, s.Svc.DeleteEntry(s.actor(r), pathID(r, "id")), "/")
}

// ---------------------------------------------------------------- export

type export struct {
	ExportedAt   time.Time        `json:"exported_at"`
	Campaign     *db.Campaign     `json:"campaign"`
	Agents       []db.Agent       `json:"agents"`
	Contacts     []db.Contact     `json:"contacts"`
	Sessions     []db.Session     `json:"sessions"`
	Adversaries  []db.Adversary   `json:"adversaries"`
	Territories  []db.Territory   `json:"territories"`
	Clocks       []db.Clock       `json:"clocks"`
	HouseRulings []db.HouseRuling `json:"house_rulings"`
	SeerNotes    []db.SeerNote    `json:"seer_notes,omitempty"`
	Entries      []db.Entry       `json:"entries"`
}

// gather collects everything the actor may see (players get the redacted view).
func (s *Server) gather(a campaign.Actor, cid uint) (*export, error) {
	c, err := s.Svc.Campaign(a, cid)
	if err != nil {
		return nil, err
	}
	x := &export{ExportedAt: time.Now(), Campaign: c}
	x.Agents, _ = s.Svc.Agents(a, cid)
	for _, ag := range x.Agents {
		cs, _ := s.Svc.Contacts(a, ag.ID)
		x.Contacts = append(x.Contacts, cs...)
	}
	_ = s.Svc.List(a, "session", cid, &x.Sessions, "number")
	_ = s.Svc.List(a, "adversary", cid, &x.Adversaries, "name")
	_ = s.Svc.List(a, "territory", cid, &x.Territories, "id")
	_ = s.Svc.List(a, "clock", cid, &x.Clocks, "name")
	_ = s.Svc.List(a, "house_ruling", cid, &x.HouseRulings, "created_at")
	if a.IsSeer() {
		_ = s.Svc.List(a, "seer_note", cid, &x.SeerNotes, "title")
	}
	x.Entries, _ = s.Svc.Entries(a, cid, "", 0)
	return x, nil
}

func (s *Server) exportJSON(w http.ResponseWriter, r *http.Request) {
	x, err := s.gather(s.actor(r), pathID(r, "cid"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", slug(x.Campaign.Name)+".json"))
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(x)
}

func (s *Server) exportMarkdown(w http.ResponseWriter, r *http.Request) {
	x, err := s.gather(s.actor(r), pathID(r, "cid"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", slug(x.Campaign.Name)+".md"))
	w.Write([]byte(renderMarkdown(x, s.Data.Current())))
}

func slug(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	return strings.Trim(b.String(), "-")
}
