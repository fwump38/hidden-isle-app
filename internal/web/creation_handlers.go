package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/fwump38/hidden-isle-app/internal/campaign"
	"github.com/fwump38/hidden-isle-app/internal/chat"
	"github.com/fwump38/hidden-isle-app/internal/creation"
	"github.com/fwump38/hidden-isle-app/internal/db"
	"github.com/fwump38/hidden-isle-app/internal/gamedata"
)

// The richer creation flow, for a player who wants help rather than typing straight into the
// quick form on the campaign page. The path is chosen up front: Step by step (the creation
// wizard, one screen per step of pp. 40-41, offering the book's options and, when the in-app
// assistant is configured, more ideas from Claude in the background) or Automatic (a
// deterministic generator does it all with real card draws, no Claude needed). Every Agent needs
// a valid class from the moment it's created (the rules data validates it), so both paths start
// on the same class page; the Agent is created there, named "New Agent" until the wizard's first
// screen names it. Both end up at the same sheet, editable exactly the same way afterward.
func (s *Server) registerCreation(mux *http.ServeMux) {
	u := func(h http.HandlerFunc) http.Handler { return s.requireUser(h) }
	mux.Handle("GET /agents/create", u(s.createPathPage))
	mux.Handle("GET /agents/create/class", u(s.createClassPage))
	mux.Handle("POST /agents/create/class", u(s.createClassSubmit))
	mux.Handle("POST /agents/create/suggest", u(s.createClassSuggest))
}

type createPathData struct {
	CampaignID  uint
	Campaign    *db.Campaign
	ChatEnabled bool
}

func (s *Server) resolveCampaignParam(a campaign.Actor, r *http.Request) (uint, *db.Campaign, error) {
	cid, err := strconv.ParseUint(r.URL.Query().Get("campaign_id"), 10, 64)
	if err != nil || cid == 0 {
		return 0, nil, nil
	}
	c, err := s.Svc.Campaign(a, uint(cid))
	if err != nil {
		return 0, nil, err
	}
	return c.ID, c, nil
}

func (s *Server) createPathPage(w http.ResponseWriter, r *http.Request) {
	a := s.actor(r)
	d := createPathData{ChatEnabled: s.Chat != nil}
	cid, c, err := s.resolveCampaignParam(a, r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	d.CampaignID, d.Campaign = cid, c
	s.render(w, r, "creation-path", http.StatusOK, pageData{Title: "Create an Agent", Error: takeFlash(w, r), Data: d})
}

type createAgentData struct {
	Path        string // "step" or "automatic"
	CampaignID  uint
	Campaign    *db.Campaign
	AllPlayers  []db.User
	Classes     []gamedata.Class
	ChatEnabled bool
	Agent       *db.Agent // set when going back from the wizard to change an Agent's class
}

// normalizePath reads the creation path; "guided" and "manual" are the old names of the
// step-by-step path, still accepted from old links.
func normalizePath(v string) string {
	if v == "automatic" {
		return v
	}
	return "step"
}

func (s *Server) createClassPage(w http.ResponseWriter, r *http.Request) {
	a := s.actor(r)
	snap := s.Data.Current()
	if snap == nil {
		s.fail(w, r, campaign.ErrNoData)
		return
	}
	d := createAgentData{Path: normalizePath(r.URL.Query().Get("path")), Classes: snap.Classes.Classes, ChatEnabled: s.Chat != nil}
	if aid, err := strconv.ParseUint(r.URL.Query().Get("agent"), 10, 64); err == nil && aid > 0 {
		ag, err := s.Svc.Agent(a, uint(aid))
		if err != nil || !s.Svc.CanEditAgent(a, ag) {
			s.fail(w, r, orForbidden(err))
			return
		}
		d.Agent, d.Path = ag, "step"
	}
	cid, c, err := s.resolveCampaignParam(a, r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	d.CampaignID, d.Campaign = cid, c
	if a.IsSeer() && d.Agent == nil {
		s.DB.Where("role = ? AND active = ?", db.RolePlayer, true).Order("name").Find(&d.AllPlayers)
	}
	s.render(w, r, "create-agent", http.StatusOK, pageData{Title: "Choose a class", Error: takeFlash(w, r), Data: d})
}

func orForbidden(err error) error {
	if err == nil {
		return campaign.ErrForbidden
	}
	return err
}

func (s *Server) createClassSubmit(w http.ResponseWriter, r *http.Request) {
	a := s.actor(r)
	path := normalizePath(r.FormValue("path"))
	cid, _ := strconv.ParseUint(r.FormValue("campaign_id"), 10, 64)
	back := fmt.Sprintf("/agents/create/class?path=%s&campaign_id=%d", path, cid)
	snap := s.Data.Current()
	if snap == nil {
		s.done(w, r, campaign.ErrNoData, back)
		return
	}
	classID := r.FormValue("class")
	if r.FormValue("random") == "1" {
		c, err := creation.RandomClass(snap.Classes.Classes)
		if err != nil {
			s.done(w, r, err, back)
			return
		}
		classID = c.ID
	}
	if strings.TrimSpace(classID) == "" {
		s.done(w, r, fmt.Errorf("pick a class"), back)
		return
	}
	if aid, err := strconv.ParseUint(r.FormValue("agent_id"), 10, 64); err == nil && aid > 0 {
		s.changeClass(w, r, a, uint(aid), classID)
		return
	}
	var owner *uint
	if n, err := strconv.ParseUint(r.FormValue("owner_id"), 10, 64); err == nil && n > 0 {
		o := uint(n)
		owner = &o
	}
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		name = unnamed
	}
	ag, err := s.Svc.NewAgent(a, uint(cid), name, classID, owner, campaign.Opts{})
	if err != nil {
		s.done(w, r, err, back)
		return
	}
	if path == "automatic" {
		redirect, err := s.runAutomatic(a, ag, snap)
		if err != nil {
			s.done(w, r, err, fmt.Sprintf("/agents/%d/wizard", ag.ID))
			return
		}
		http.Redirect(w, r, redirect, http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/agents/%d/wizard", ag.ID), http.StatusSeeOther)
}

// changeClass is the wizard's Back from its first screen: the player picked a different class
// for an Agent they're still creating (campaign.Service.ChangeClass says what that resets).
func (s *Server) changeClass(w http.ResponseWriter, r *http.Request, a campaign.Actor, agentID uint, classID string) {
	_, err := s.Svc.ChangeClass(a, agentID, classID, campaign.Opts{})
	s.done(w, r, err, fmt.Sprintf("/agents/%d/wizard?step=name", agentID))
}

// createClassSuggest asks Claude, in the background, which classes fit what the player describes.
func (s *Server) createClassSuggest(w http.ResponseWriter, r *http.Request) {
	a := s.actor(r)
	_ = r.ParseForm()
	d := wizSuggestions{Exclude: r.Form["exclude"]}
	snap := s.Data.Current()
	switch {
	case s.Chat == nil:
		d.Error = "Suggestions need the in-app assistant, which isn't set up."
	case snap == nil:
		d.Error = friendly(campaign.ErrNoData)
	case strings.TrimSpace(r.FormValue("hint")) == "":
		d.Error = "Describe the character you have in mind, or how you like to play, first."
	}
	if d.Error != "" {
		s.partial(w, "create-agent", "choices", d)
		return
	}
	req := chat.SuggestRequest{Kind: "class", Hint: r.FormValue("hint"), Exclude: d.Exclude}
	names := make([]string, 0, len(snap.Classes.Classes))
	for _, c := range snap.Classes.Classes {
		req.Context = append(req.Context, fmt.Sprintf("%s (%s): %s", c.Name, c.Guild, c.Summary))
		names = append(names, c.Name)
	}
	// Force the model to pick one of the real classes: with a free-text field, a strongly
	// off-list concept (e.g. "a grave digger dabbling in necromancy") can tempt it into inventing
	// a plausible-sounding class name that then matches nothing and vanishes silently.
	req.Enum = map[string][]string{"class": names}
	sugs, err := s.Chat.Suggest(r.Context(), a.User, req)
	if err != nil {
		d.Error = sentence(friendly(err))
	}
	for _, sg := range sugs {
		if c, ok := wizChoiceFor("class", sg, nil, nil, snap, nil); ok {
			d.Choices = append(d.Choices, c)
			d.Exclude = append(d.Exclude, c.Label)
		}
	}
	if d.Error == "" && len(d.Choices) == 0 {
		d.Error = "Nothing usable came back. Try again, or describe what you're after."
	}
	s.partial(w, "create-agent", "choices", d)
}

// runAutomatic runs the deterministic Automatic path (pp. 40-41) on a freshly created Agent and
// returns where to send the player next.
func (s *Server) runAutomatic(a campaign.Actor, ag *db.Agent, snap *gamedata.Snapshot) (string, error) {
	class := snap.Class(ag.Class)
	solo := false
	if ag.CampaignID != 0 {
		if c, err := s.Svc.Campaign(a, ag.CampaignID); err == nil && c.Mode == "solitaire" {
			solo = true
		}
	}
	res, err := creation.Generate(snap, class, solo)
	if err != nil {
		return "", err
	}
	note := "Automatically created (pp. 40-41):\n" + strings.Join(res.Log, "\n")
	if strings.TrimSpace(ag.Notes) != "" {
		note = ag.Notes + "\n\n" + note
	}
	res.Fields["notes"] = note
	patch, err := fieldsPatch(res.Fields)
	if err != nil {
		return "", err
	}
	if _, err := s.Svc.Update(a, "agent", ag.ID, patch, campaign.Opts{Reason: "automatic creation (pp. 40-41)"}); err != nil {
		return "", err
	}
	for _, c := range res.Contacts {
		cp, err := fieldsPatch(map[string]any{
			"agent_id": ag.ID, "kind": c.Kind, "name": c.Name, "card": c.Card, "land": c.Land,
			"description": c.Description, "affection": c.Affection,
		})
		if err != nil {
			return "", err
		}
		if _, err := s.Svc.CreateRecord(a, "contact", ag.CampaignID, cp, campaign.Opts{Reason: "automatic creation (p. 41)"}); err != nil {
			return "", err
		}
	}
	return fmt.Sprintf("/agents/%d/wizard?step=done", ag.ID), nil
}

// fieldsPatch turns plain field→value pairs into a campaign.Patch (same shape the MCP tools and
// the in-app chat use for the same purpose).
func fieldsPatch(fields map[string]any) (campaign.Patch, error) {
	p := campaign.Patch{}
	for k, v := range fields {
		b, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		p[k] = b
	}
	return p, nil
}
