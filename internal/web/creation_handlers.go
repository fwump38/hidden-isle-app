package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/fwump38/hidden-isle-app/internal/campaign"
	"github.com/fwump38/hidden-isle-app/internal/creation"
	"github.com/fwump38/hidden-isle-app/internal/db"
	"github.com/fwump38/hidden-isle-app/internal/gamedata"
)

// The richer creation flow, for a player who wants help rather than typing straight into the
// quick form on the home or campaign page. It's three steps: pick a class (with descriptions
// and an optional "help me choose" chat), then pick how to fill in the rest — Guided (chat walks
// you through it), Automatic (a deterministic generator does it, no chat needed), or Manual (the
// existing step-by-step wizard, unchanged). All three end up at the same sheet, editable exactly
// the same way afterward.
func (s *Server) registerCreation(mux *http.ServeMux) {
	u := func(h http.HandlerFunc) http.Handler { return s.requireUser(h) }
	mux.Handle("GET /agents/create", u(s.createAgentPage))
	mux.Handle("POST /agents/create", u(s.createAgentSubmit))
	mux.Handle("GET /agents/{id}/creation-path", u(s.creationPathPage))
	mux.Handle("POST /agents/{id}/creation/automatic", u(s.createAutomatic))
}

type createAgentData struct {
	CampaignID    uint
	Campaign      *db.Campaign
	AllPlayers    []db.User
	Classes       []gamedata.Class
	ChatEnabled   bool
	HelpChooseURL string
}

func (s *Server) createAgentPage(w http.ResponseWriter, r *http.Request) {
	a := s.actor(r)
	snap := s.Data.Current()
	if snap == nil {
		s.fail(w, r, campaign.ErrNoData)
		return
	}
	d := createAgentData{Classes: snap.Classes.Classes, ChatEnabled: s.Chat != nil}
	chatBase := "/chat"
	if cid, err := strconv.ParseUint(r.URL.Query().Get("campaign_id"), 10, 64); err == nil && cid > 0 {
		c, err := s.Svc.Campaign(a, uint(cid))
		if err != nil {
			s.fail(w, r, err)
			return
		}
		d.CampaignID, d.Campaign = c.ID, c
		chatBase = fmt.Sprintf("/c/%d/chat", c.ID)
	}
	d.HelpChooseURL = chatBase + "?ask=" + url.QueryEscape("Help me pick a Hidden Isle class. Here's what I'm thinking: ")
	if a.IsSeer() {
		s.DB.Where("role = ? AND active = ?", db.RolePlayer, true).Order("name").Find(&d.AllPlayers)
	}
	s.render(w, r, "create-agent", http.StatusOK, pageData{Title: "Create an Agent", Error: takeFlash(w, r), Data: d})
}

func (s *Server) createAgentSubmit(w http.ResponseWriter, r *http.Request) {
	a := s.actor(r)
	cid, _ := strconv.ParseUint(r.FormValue("campaign_id"), 10, 64)
	back := fmt.Sprintf("/agents/create?campaign_id=%d", cid)
	var owner *uint
	if n, err := strconv.ParseUint(r.FormValue("owner_id"), 10, 64); err == nil && n > 0 {
		o := uint(n)
		owner = &o
	}
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		name = "New Agent"
	}
	ag, err := s.Svc.NewAgent(a, uint(cid), name, r.FormValue("class"), owner, campaign.Opts{})
	if err != nil {
		s.done(w, r, err, back)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/agents/%d/creation-path", ag.ID), http.StatusSeeOther)
}

type creationPathData struct {
	Agent       *db.Agent
	Class       *gamedata.Class
	ChatEnabled bool
	ChatURL     string // where "Guided" sends the player, with a pre-filled first message
}

func (s *Server) creationPathPage(w http.ResponseWriter, r *http.Request) {
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
	d := creationPathData{Agent: ag, ChatEnabled: s.Chat != nil}
	if snap != nil {
		d.Class = snap.Class(ag.Class)
	}
	base := "/chat"
	if ag.CampaignID != 0 {
		base = fmt.Sprintf("/c/%d/chat", ag.CampaignID)
	}
	className := ag.Class
	if d.Class != nil {
		className = d.Class.Name
	}
	ask := fmt.Sprintf("I'm creating my Agent %s, a %s. Let's go through core self, burden, ideal, abilities, skills and the rest step by step (pp. 40-41).", ag.Name, className)
	d.ChatURL = base + "?ask=" + url.QueryEscape(ask)
	s.render(w, r, "creation-path", http.StatusOK, pageData{Title: "Create " + ag.Name, Error: takeFlash(w, r), Data: d})
}

func (s *Server) createAutomatic(w http.ResponseWriter, r *http.Request) {
	a := s.actor(r)
	id := pathID(r, "id")
	back := fmt.Sprintf("/agents/%d/creation-path", id)
	ag, err := s.Svc.Agent(a, id)
	if err != nil {
		s.done(w, r, err, back)
		return
	}
	if !s.Svc.CanEditAgent(a, ag) {
		s.done(w, r, campaign.ErrForbidden, back)
		return
	}
	snap := s.Data.Current()
	if snap == nil {
		s.done(w, r, campaign.ErrNoData, back)
		return
	}
	class := snap.Class(ag.Class)
	solo := false
	if ag.CampaignID != 0 {
		if c, err := s.Svc.Campaign(a, ag.CampaignID); err == nil && c.Mode == "solitaire" {
			solo = true
		}
	}
	res, err := creation.Generate(snap, class, solo)
	if err != nil {
		s.done(w, r, err, back)
		return
	}
	note := "Automatically created (pp. 40-41):\n" + strings.Join(res.Log, "\n")
	if strings.TrimSpace(ag.Notes) != "" {
		note = ag.Notes + "\n\n" + note
	}
	res.Fields["notes"] = note
	patch, err := fieldsPatch(res.Fields)
	if err != nil {
		s.done(w, r, err, back)
		return
	}
	if _, err := s.Svc.Update(a, "agent", id, patch, campaign.Opts{Reason: "automatic creation (pp. 40-41)"}); err != nil {
		s.done(w, r, err, back)
		return
	}
	for _, c := range res.Contacts {
		cp, err := fieldsPatch(map[string]any{
			"agent_id": id, "kind": c.Kind, "name": c.Name, "card": c.Card, "land": c.Land,
			"description": c.Description, "affection": c.Affection,
		})
		if err != nil {
			s.done(w, r, err, back)
			return
		}
		if _, err := s.Svc.CreateRecord(a, "contact", ag.CampaignID, cp, campaign.Opts{Reason: "automatic creation (p. 41)"}); err != nil {
			s.done(w, r, err, back)
			return
		}
	}
	http.Redirect(w, r, fmt.Sprintf("/agents/%d/wizard?step=12", id), http.StatusSeeOther)
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
