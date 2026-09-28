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
// quick form on the campaign page. The path is chosen up front, before class or anything else:
// Guided (chat walks you through it, after you pick a class), Automatic (a deterministic
// generator does it, no chat needed, after you pick a class or draw a random one), or Manual
// (the existing step-by-step sheet, after you pick a class the plain way). Every Agent needs a
// valid class from the moment it's created (the rules data validates it), so all three paths
// pick one on the same class page; only Automatic offers a random draw, and only Guided and
// Automatic offer chat's "help me choose". All three end up at the same sheet, editable exactly
// the same way afterward.
func (s *Server) registerCreation(mux *http.ServeMux) {
	u := func(h http.HandlerFunc) http.Handler { return s.requireUser(h) }
	mux.Handle("GET /agents/create", u(s.createPathPage))
	mux.Handle("GET /agents/create/class", u(s.createClassPage))
	mux.Handle("POST /agents/create/class", u(s.createClassSubmit))
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
	Path          string // "guided", "automatic" or "manual"
	CampaignID    uint
	Campaign      *db.Campaign
	AllPlayers    []db.User
	Classes       []gamedata.Class
	ChatEnabled   bool
	HelpChooseURL string
}

func normalizePath(v string) string {
	switch v {
	case "automatic", "manual":
		return v
	default:
		return "guided"
	}
}

func (s *Server) createClassPage(w http.ResponseWriter, r *http.Request) {
	a := s.actor(r)
	snap := s.Data.Current()
	if snap == nil {
		s.fail(w, r, campaign.ErrNoData)
		return
	}
	d := createAgentData{Path: normalizePath(r.URL.Query().Get("path")), Classes: snap.Classes.Classes, ChatEnabled: s.Chat != nil}
	cid, c, err := s.resolveCampaignParam(a, r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	d.CampaignID, d.Campaign = cid, c
	chatBase := "/chat"
	if c != nil {
		chatBase = fmt.Sprintf("/c/%d/chat", c.ID)
	}
	d.HelpChooseURL = chatBase + "?ask=" + url.QueryEscape("Help me pick a Hidden Isle class. Here's what I'm thinking: ")
	if a.IsSeer() {
		s.DB.Where("role = ? AND active = ?", db.RolePlayer, true).Order("name").Find(&d.AllPlayers)
	}
	s.render(w, r, "create-agent", http.StatusOK, pageData{Title: "Choose a class", Error: takeFlash(w, r), Data: d})
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
	var owner *uint
	if n, err := strconv.ParseUint(r.FormValue("owner_id"), 10, 64); err == nil && n > 0 {
		o := uint(n)
		owner = &o
	}
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		name = "New Agent"
	}
	ag, err := s.Svc.NewAgent(a, uint(cid), name, classID, owner, campaign.Opts{})
	if err != nil {
		s.done(w, r, err, back)
		return
	}
	switch path {
	case "guided":
		http.Redirect(w, r, guidedChatURL(ag, snap), http.StatusSeeOther)
	case "manual":
		http.Redirect(w, r, fmt.Sprintf("/agents/%d/wizard", ag.ID), http.StatusSeeOther)
	default: // automatic
		redirect, err := s.runAutomatic(a, ag, snap)
		if err != nil {
			s.done(w, r, err, fmt.Sprintf("/agents/%d/wizard", ag.ID))
			return
		}
		http.Redirect(w, r, redirect, http.StatusSeeOther)
	}
}

// guidedChatURL is where the Guided path sends the player once their class is chosen, with a
// pre-filled first message.
func guidedChatURL(ag *db.Agent, snap *gamedata.Snapshot) string {
	base := "/chat"
	if ag.CampaignID != 0 {
		base = fmt.Sprintf("/c/%d/chat", ag.CampaignID)
	}
	className := ag.Class
	if c := snap.Class(ag.Class); c != nil {
		className = c.Name
	}
	ask := fmt.Sprintf("I'm creating my Agent %s, a %s. Let's go through core self, burden, ideal, abilities, skills and the rest step by step (pp. 40-41).", ag.Name, className)
	return base + "?ask=" + url.QueryEscape(ask)
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
	return fmt.Sprintf("/agents/%d/wizard?step=12", ag.ID), nil
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
