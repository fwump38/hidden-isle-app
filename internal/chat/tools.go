package chat

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go"

	"github.com/fwump38/hidden-isle-app/internal/campaign"
	"github.com/fwump38/hidden-isle-app/internal/db"
)

// toolDefs is the fixed, player-scoped, read-mostly allowlist. Every handler runs as the
// player's own Actor{Via: "chat"}, so campaign.Service's own permission checks apply — a chat
// tool can't see or change anything the player couldn't already see or change themselves.
func (s *Service) toolDefs() []anthropic.ToolUnionParam {
	return []anthropic.ToolUnionParam{
		newTool("list_my_agents", "The player's own Agents (id, name, class, status).", map[string]any{}),
		newTool("get_agent", "One Agent's full sheet: skills, harm, tracks, abilities (verbatim text), proficiencies, items. Works for the player's own Agents and any Agent in a shared campaign (p. 28).",
			map[string]any{"agent_id": intProp("the Agent's id")}, "agent_id"),
		newTool("list_contacts", "One Agent's contacts (name, card, affection, distance).",
			map[string]any{"agent_id": intProp("the Agent's id")}, "agent_id"),
		newTool("campaign_overview", "The current campaign: settings, party members, public clocks and adversaries, house rulings, and published recaps.", map[string]any{}),
		newTool("search_rules", "Search the rules text (condensed and full editions) for sections, with page cites. Never includes adventures.",
			map[string]any{"query": strProp("words to search for"), "limit": intProp("max results, default 10")}, "query"),
		newTool("read_rules", "Read a rules section by the ref from search_rules, or a whole file by path.",
			map[string]any{"ref": strProp("a ref from search_rules, or a file path")}, "ref"),
		newTool("get_class", "A class with its motto, pre-filled skills, items and every ability, verbatim.",
			map[string]any{"class": strProp("class name or id")}, "class"),
		newTool("lookup_card", "A vision card's Vision Guide entry, Character History phrases and burdens/ideals.",
			map[string]any{"name": strProp("the card's name")}, "name"),
		newTool("get_table", "A structured rules table: limits, oracle, downtime, setting or skills.",
			map[string]any{"name": strProp("limits | oracle | downtime | setting | skills")}, "name"),
		newTool("suggest_agent_change", "Propose a change to the player's own Agent. This only stages the change as a card in the chat; it takes effect only if the player taps Apply. fields uses the sheet's field names (e.g. look, culture, why, skills, vices, magical_sources, items).",
			map[string]any{
				"agent_id": intProp("the player's own Agent id"),
				"fields":   objProp("field name → new value, e.g. {\"look\": \"...\", \"culture\": \"...\"}"),
				"summary":  strProp("one short sentence describing the change, for the player to read"),
				"why":      strProp("why you're suggesting it, for the change log"),
			}, "agent_id", "fields", "summary"),
		newTool("update_memory", "Replace what you remember about this player for next time: a short note (names, class, decisions, open questions), not a transcript.",
			map[string]any{"note": strProp("the full note to remember, replacing the old one")}, "note"),
	}
}

// runTool executes one tool call and returns its result text, whether it's an error, and (for
// suggest_agent_change) the suggestion it staged.
func (s *Service) runTool(a campaign.Actor, th db.ChatThread, name string, input json.RawMessage) (string, bool, *db.ChatSuggestion) {
	result, sug, err := s.dispatch(a, th, name, input)
	if err != nil {
		return err.Error(), true, nil
	}
	b, err := json.Marshal(result)
	if err != nil {
		return err.Error(), true, nil
	}
	return string(b), false, sug
}

func (s *Service) dispatch(a campaign.Actor, th db.ChatThread, name string, input json.RawMessage) (any, *db.ChatSuggestion, error) {
	switch name {
	case "list_my_agents":
		ags, err := s.svc.MyAgents(a)
		return ags, nil, err

	case "get_agent":
		var in struct {
			AgentID uint `json:"agent_id"`
		}
		if err := json.Unmarshal(input, &in); err != nil {
			return nil, nil, err
		}
		ag, err := s.svc.Agent(a, in.AgentID)
		return ag, nil, err

	case "list_contacts":
		var in struct {
			AgentID uint `json:"agent_id"`
		}
		if err := json.Unmarshal(input, &in); err != nil {
			return nil, nil, err
		}
		cs, err := s.svc.Contacts(a, in.AgentID)
		return cs, nil, err

	case "campaign_overview":
		if th.CampaignID == 0 {
			return nil, nil, fmt.Errorf("the player isn't in a campaign yet")
		}
		out, err := s.campaignOverview(a, th.CampaignID)
		return out, nil, err

	case "search_rules":
		var in struct {
			Query string `json:"query"`
			Limit int    `json:"limit"`
		}
		if err := json.Unmarshal(input, &in); err != nil {
			return nil, nil, err
		}
		if s.rules == nil {
			return nil, nil, fmt.Errorf("rules search isn't available")
		}
		hits, err := s.rules.Search(in.Query, false, in.Limit)
		return hits, nil, err

	case "read_rules":
		var in struct {
			Ref string `json:"ref"`
		}
		if err := json.Unmarshal(input, &in); err != nil {
			return nil, nil, err
		}
		if s.rules == nil {
			return nil, nil, fmt.Errorf("rules search isn't available")
		}
		secs, err := s.rules.Read(in.Ref, false)
		return secs, nil, err

	case "get_class":
		var in struct {
			Class string `json:"class"`
		}
		if err := json.Unmarshal(input, &in); err != nil {
			return nil, nil, err
		}
		snap := s.data.Current()
		if snap == nil {
			return nil, nil, fmt.Errorf("rules data hasn't loaded yet")
		}
		c := snap.Class(strings.ToLower(in.Class))
		if c == nil {
			c = snap.Class(in.Class)
		}
		if c == nil {
			return nil, nil, fmt.Errorf("no class %q", in.Class)
		}
		return c, nil, nil

	case "lookup_card":
		var in struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(input, &in); err != nil {
			return nil, nil, err
		}
		snap := s.data.Current()
		if snap == nil {
			return nil, nil, fmt.Errorf("rules data hasn't loaded yet")
		}
		q := strings.ToLower(strings.TrimSpace(in.Name))
		for _, c := range snap.Cards.Vision {
			if strings.ToLower(c.Name) == q || strings.ToLower(c.ID) == q ||
				strings.TrimPrefix(strings.ToLower(c.Name), "the ") == strings.TrimPrefix(q, "the ") {
				return c, nil, nil
			}
		}
		return nil, nil, fmt.Errorf("no vision card %q", in.Name)

	case "get_table":
		var in struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(input, &in); err != nil {
			return nil, nil, err
		}
		snap := s.data.Current()
		if snap == nil {
			return nil, nil, fmt.Errorf("rules data hasn't loaded yet")
		}
		switch in.Name {
		case "limits":
			return snap.Limits, nil, nil
		case "skills":
			return snap.Skills, nil, nil
		}
		if t, ok := snap.Raw[in.Name]; ok {
			return t, nil, nil
		}
		return nil, nil, fmt.Errorf("table must be limits, oracle, downtime, setting or skills")

	case "suggest_agent_change":
		var in struct {
			AgentID uint           `json:"agent_id"`
			Fields  map[string]any `json:"fields"`
			Summary string         `json:"summary"`
			Why     string         `json:"why"`
		}
		if err := json.Unmarshal(input, &in); err != nil {
			return nil, nil, err
		}
		ag, err := s.svc.Agent(a, in.AgentID)
		if err != nil {
			return nil, nil, err
		}
		if !s.svc.CanEditAgent(a, ag) {
			return nil, nil, fmt.Errorf("that's not the player's own Agent")
		}
		if len(in.Fields) == 0 {
			return nil, nil, fmt.Errorf("fields can't be empty")
		}
		patch, err := toPatch(in.Fields)
		if err != nil {
			return nil, nil, err
		}
		fieldsJSON, err := json.Marshal(patch)
		if err != nil {
			return nil, nil, err
		}
		sug := db.ChatSuggestion{ThreadID: th.ID, AgentID: in.AgentID, Fields: string(fieldsJSON),
			Summary: in.Summary, Why: in.Why, Status: "pending"}
		if err := s.db.Create(&sug).Error; err != nil {
			return nil, nil, err
		}
		return map[string]any{"suggestion_id": sug.ID,
			"note": "Recorded. Nothing has changed yet — the player needs to tap Apply on the card."}, &sug, nil

	case "update_memory":
		var in struct {
			Note string `json:"note"`
		}
		if err := json.Unmarshal(input, &in); err != nil {
			return nil, nil, err
		}
		if err := s.db.Model(&db.ChatThread{}).Where("id = ?", th.ID).Update("memory", strings.TrimSpace(in.Note)).Error; err != nil {
			return nil, nil, err
		}
		return map[string]string{"note": "remembered"}, nil, nil
	}
	return nil, nil, fmt.Errorf("unknown tool %q", name)
}

type campaignSummary struct {
	Campaign    *db.Campaign     `json:"campaign"`
	Agents      []db.Agent       `json:"agents"`
	Clocks      []db.Clock       `json:"clocks"`
	Adversaries []db.Adversary   `json:"adversaries"`
	Rulings     []db.HouseRuling `json:"house_rulings"`
	Recaps      []db.Entry       `json:"recaps"`
}

func (s *Service) campaignOverview(a campaign.Actor, campaignID uint) (*campaignSummary, error) {
	c, err := s.svc.Campaign(a, campaignID)
	if err != nil {
		return nil, err
	}
	out := &campaignSummary{Campaign: c}
	out.Agents, _ = s.svc.Agents(a, campaignID)
	var clocks []db.Clock
	_ = s.svc.List(a, "clock", campaignID, &clocks, "id")
	out.Clocks = clocks
	var advs []db.Adversary
	_ = s.svc.List(a, "adversary", campaignID, &advs, "id")
	out.Adversaries = advs
	var rulings []db.HouseRuling
	_ = s.svc.List(a, "house_ruling", campaignID, &rulings, "id")
	out.Rulings = rulings
	recaps, _ := s.svc.Entries(a, campaignID, "recap", 0)
	out.Recaps = recaps
	return out, nil
}

func toPatch(fields map[string]any) (campaign.Patch, error) {
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

// ApplySuggestion applies a pending suggestion through the normal validated, logged Update path.
// Only the player whose thread it is may apply it (the confirmation the plan requires).
func (s *Service) ApplySuggestion(a campaign.Actor, id uint) error {
	sug, th, err := s.ownedSuggestion(a, id)
	if err != nil {
		return err
	}
	if sug.Status != "pending" {
		return fmt.Errorf("that suggestion was already %s", sug.Status)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(sug.Fields), &fields); err != nil {
		return err
	}
	reason := sug.Why
	if reason == "" {
		reason = sug.Summary
	}
	if _, err := s.svc.Update(a, "agent", sug.AgentID, campaign.Patch(fields), campaign.Opts{Reason: "chat: " + reason}); err != nil {
		return err
	}
	_ = th
	now := time.Now()
	return s.db.Model(sug).Updates(map[string]any{"status": "applied", "decided_at": &now}).Error
}

// DismissSuggestion drops a pending suggestion without applying it.
func (s *Service) DismissSuggestion(a campaign.Actor, id uint) error {
	sug, _, err := s.ownedSuggestion(a, id)
	if err != nil {
		return err
	}
	if sug.Status != "pending" {
		return nil
	}
	now := time.Now()
	return s.db.Model(sug).Updates(map[string]any{"status": "dismissed", "decided_at": &now}).Error
}

func (s *Service) ownedSuggestion(a campaign.Actor, id uint) (*db.ChatSuggestion, *db.ChatThread, error) {
	var sug db.ChatSuggestion
	if err := s.db.First(&sug, id).Error; err != nil {
		return nil, nil, err
	}
	var th db.ChatThread
	if err := s.db.First(&th, sug.ThreadID).Error; err != nil {
		return nil, nil, err
	}
	if a.User == nil || th.UserID != a.User.ID {
		return nil, nil, campaign.ErrForbidden
	}
	return &sug, &th, nil
}

// UpdateMemory lets the player (or the Seer, reading along) edit the remembered note by hand.
func (s *Service) UpdateMemory(a campaign.Actor, threadID uint, note string) error {
	var th db.ChatThread
	if err := s.db.First(&th, threadID).Error; err != nil {
		return err
	}
	if th.UserID != a.User.ID && !a.IsSeer() {
		return campaign.ErrForbidden
	}
	return s.db.Model(&th).Update("memory", strings.TrimSpace(note)).Error
}
