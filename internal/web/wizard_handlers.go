package web

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/fwump38/hidden-isle-app/internal/campaign"
	"github.com/fwump38/hidden-isle-app/internal/cards"
	"github.com/fwump38/hidden-isle-app/internal/db"
	"github.com/fwump38/hidden-isle-app/internal/gamedata"
	"github.com/fwump38/hidden-isle-app/internal/oracle"
)

// The creation wizard walks a player through pp. 40-42 (Ref p. 7) one step at a time. It's a
// thin layer over the sheet's own endpoints: every field it saves goes through the same
// POST /agents/{id} and /agents/{id}/contacts as the sheet, so it gets the same permission
// checks, validation and change log for free. The wizard itself only decides which step to
// show and offers card draws; it doesn't duplicate the sheet's write logic.
//
// There's no stored "current step": it's inferred from what's already filled in, so leaving
// and coming back (or a Seer helping mid-way) always lands somewhere sensible. Every step is
// still reachable directly from the stepper, so nobody gets stuck.

func (s *Server) registerWizard(mux *http.ServeMux) {
	u := func(h http.HandlerFunc) http.Handler { return s.requireUser(h) }
	mux.Handle("GET /agents/{id}/wizard", u(s.wizardPage))
	mux.Handle("POST /agents/{id}/wizard/draw", u(s.wizardDraw))
}

type wizStepInfo struct {
	N     int
	Title string
}

var wizStepList = []wizStepInfo{
	{1, "Childhood"}, {2, "Adulthood"}, {3, "Burden"}, {4, "Ideal"}, {5, "Abilities"},
	{6, "Skills"}, {7, "Magic"}, {8, "Who they are"}, {9, "Why Dioscoria"},
	{10, "Homeland contact"}, {11, "Dioscorian contact"}, {12, "Done"},
}

// wizardData is what the wizard template renders.
type wizardData struct {
	Agent      *db.Agent
	Class      *gamedata.Class
	Solo       bool
	Limits     *gamedata.Limits
	Step       int
	Steps      []wizStepInfo
	Done       map[int]bool
	Vision     []string
	Regions    []string
	Drawn      []string // this request's digital draw, if any
	DrawFor    string   // which field the draw was for
	Homeland   *db.Contact
	Dioscorian *db.Contact
	Chosen     map[string]bool // ability id -> already on the sheet
	Error      string
}

// creationStep works out the first incomplete step for ag (1-12; 12 = everything's there).
func creationStep(ag *db.Agent, class *gamedata.Class, l *gamedata.Limits, solo bool, contacts []db.Contact) int {
	switch {
	case strings.TrimSpace(ag.ChildPhrase) == "":
		return 1
	case strings.TrimSpace(ag.AdultPhrase) == "" || ag.AdultVerb == "":
		return 2
	case strings.TrimSpace(ag.Burden) == "":
		return 3
	case strings.TrimSpace(ag.Ideal) == "":
		return 4
	case len(ag.Abilities) < l.Creation.Abilities:
		return 5
	case skillTotal(ag) < creationTotalPoints(l, solo):
		return 6
	}
	if class != nil && class.StartsWithAdeptProficiency && len(ag.Proficiencies) == 0 {
		return 7
	}
	switch {
	case strings.TrimSpace(ag.Look) == "":
		return 8
	case strings.TrimSpace(ag.Why) == "":
		return 9
	}
	if !hasContactKind(contacts, "Homeland") {
		return 10
	}
	if !hasContactKind(contacts, "Dioscorian") {
		return 11
	}
	return 12
}

func skillTotal(ag *db.Agent) int {
	n := 0
	for _, v := range ag.Skills {
		n += v
	}
	return n
}

func creationTotalPoints(l *gamedata.Limits, solo bool) int {
	if solo && l.Creation.Solo.TotalPoints > 0 {
		return l.Creation.Solo.TotalPoints
	}
	return l.Creation.TotalPoints
}

func creationMaxSkill(l *gamedata.Limits, solo bool) int {
	if solo && l.Creation.Solo.MaxSkill > 0 {
		return l.Creation.Solo.MaxSkill
	}
	return l.Creation.MaxSkill
}

func hasContactKind(contacts []db.Contact, kind string) bool {
	for _, c := range contacts {
		if c.Kind == kind {
			return true
		}
	}
	return false
}

func (s *Server) wizardPage(w http.ResponseWriter, r *http.Request) {
	a := s.actor(r)
	ag, err := s.Svc.Agent(a, pathID(r, "id"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if !s.Svc.CanEditAgent(a, ag) {
		s.fail(w, r, campaign.ErrForbidden)
		return
	}
	snap := s.Data.Current()
	if snap == nil {
		s.fail(w, r, campaign.ErrNoData)
		return
	}
	d := s.buildWizardData(a, ag, snap)
	if n, err := strconv.Atoi(r.URL.Query().Get("step")); err == nil && n >= 1 && n <= 12 {
		d.Step = n
	}
	var c *db.Campaign
	if ag.CampaignID != 0 {
		c, _ = s.Svc.Campaign(a, ag.CampaignID)
	}
	s.render(w, r, "wizard", http.StatusOK, pageData{Title: "Create " + ag.Name, Error: takeFlash(w, r), Data: d,
		Nav: &campaignNav{Campaign: c, Section: "agents", Snap: snap}})
}

func (s *Server) buildWizardData(a campaign.Actor, ag *db.Agent, snap *gamedata.Snapshot) wizardData {
	class := snap.Class(ag.Class)
	solo := false
	if ag.CampaignID != 0 {
		if c, err := s.Svc.Campaign(a, ag.CampaignID); err == nil && c.Mode == "solitaire" {
			solo = true
		}
	}
	contacts, _ := s.Svc.Contacts(a, ag.ID)
	chosen := map[string]bool{}
	for _, ab := range ag.Abilities {
		if ab.ID != "" {
			chosen[ab.ID] = true
		}
	}
	d := wizardData{Agent: ag, Class: class, Solo: solo, Limits: &snap.Limits, Steps: wizStepList,
		Done: map[int]bool{}, Homeland: findContact(contacts, "Homeland"), Dioscorian: findContact(contacts, "Dioscorian"), Chosen: chosen}
	current := creationStep(ag, class, &snap.Limits, solo, contacts)
	for _, st := range wizStepList {
		d.Done[st.N] = st.N < current
	}
	d.Step = current
	for _, v := range snap.Cards.Vision {
		d.Vision = append(d.Vision, v.Name)
	}
	if t, err := oracle.Load(snap); err == nil {
		d.Regions = t.Regions()
	}
	return d
}

func findContact(contacts []db.Contact, kind string) *db.Contact {
	for i := range contacts {
		if contacts[i].Kind == kind {
			return &contacts[i]
		}
	}
	return nil
}

// wizardDraw draws cards digitally for a step and re-renders the wizard with the draw shown.
// The table normally draws real cards; this is only the backup, same as everywhere else.
func (s *Server) wizardDraw(w http.ResponseWriter, r *http.Request) {
	a := s.actor(r)
	ag, err := s.Svc.Agent(a, pathID(r, "id"))
	if err != nil || !s.Svc.CanEditAgent(a, ag) {
		s.fail(w, r, err)
		return
	}
	snap := s.Data.Current()
	if snap == nil {
		s.fail(w, r, campaign.ErrNoData)
		return
	}
	_ = r.ParseForm()
	deck := orDefault(r.FormValue("deck"), "vision")
	n, _ := strconv.Atoi(orDefault(r.FormValue("count"), "1"))
	if n < 1 {
		n = 1
	}
	d := s.buildWizardData(a, ag, snap)
	if step, err := strconv.Atoi(r.FormValue("step")); err == nil {
		d.Step = step
	}
	hands, err := cards.Draw(snap, deck, []cards.Request{{Count: n}})
	if err != nil {
		d.Error = err.Error()
	} else {
		d.Drawn = hands[0].Cards
		d.DrawFor = r.FormValue("for")
	}
	s.partial(w, "wizard", "wizard-body", d)
}
