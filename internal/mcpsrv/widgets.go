package mcpsrv

import (
	"context"
	"embed"
	"fmt"
	"slices"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/fwump38/hidden-isle-app/internal/campaign"
	"github.com/fwump38/hidden-isle-app/internal/challenge"
	"github.com/fwump38/hidden-isle-app/internal/db"
)

// MCP Apps widgets: small HTML views that claude.ai renders inline when a show_* tool runs.
// Each widget is a ui:// resource; its tool names it in _meta.ui.resourceUri. The page talks to
// the host over postMessage (JSON-RPC): the host hands it the tool's structured result, and the
// page calls the ordinary tools (tick_clock, update_record, challenge_count…) to make changes, so
// limits, permissions and the change log apply exactly as they do everywhere else.

//go:embed widgets
var widgetFS embed.FS

// appMIME is the MCP Apps resource type for an HTML view.
const appMIME = "text/html;profile=mcp-app"

func widgetURI(name string) string { return "ui://hidden-isle/" + name + ".html" }

var widgets = []struct{ name, title, desc string }{
	{"agent-sheet", "Agent sheet", "An Agent's live character sheet: skills, harm, XP, tracks, abilities, contacts and clocks."},
	{"clocks", "Clocks", "A campaign's clocks, to tick, reveal or start."},
	{"challenge", "Challenge helper", "Count both hands and resolve the cards played for one Agent (pp. 15-19)."},
}

// widgetHTML builds one self-contained page: the widget's own file with the shared style and
// script inlined (the host's sandbox loads nothing else).
func widgetHTML(name string) (string, error) {
	page, err := widgetFS.ReadFile("widgets/" + name + ".html")
	if err != nil {
		return "", err
	}
	css, err := widgetFS.ReadFile("widgets/shared.css")
	if err != nil {
		return "", err
	}
	js, err := widgetFS.ReadFile("widgets/shared.js")
	if err != nil {
		return "", err
	}
	out := strings.Replace(string(page), "/*SHARED_CSS*/", string(css), 1)
	return strings.Replace(out, "/*SHARED_JS*/", string(js), 1), nil
}

func (s *Server) addWidgets() {
	for _, w := range widgets {
		html, err := widgetHTML(w.name)
		if err != nil {
			panic(err) // embedded files: a build error, not a runtime one
		}
		uri := widgetURI(w.name)
		meta := mcp.Meta{"ui": map[string]any{"prefersBorder": true}}
		s.mcp.AddResource(&mcp.Resource{URI: uri, Name: w.name, Title: w.title, Description: w.desc, MIMEType: appMIME, Meta: meta},
			func(context.Context, *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
				return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: uri, MIMEType: appMIME, Text: html, Meta: meta}}}, nil
			})
	}
}

// uiTool registers a tool whose result the host shows in a widget. "ui/resourceUri" is the
// older flat key, kept for hosts that haven't moved to _meta.ui yet.
func uiTool[In any](s *Server, name, widget, desc string, h func(ctx context.Context, a campaign.Actor, in In) (any, error)) {
	uri := widgetURI(widget)
	mcp.AddTool(s.mcp, &mcp.Tool{Name: name, Description: desc,
		Meta: mcp.Meta{"ui": map[string]any{"resourceUri": uri}, "ui/resourceUri": uri}},
		func(ctx context.Context, req *mcp.CallToolRequest, in In) (*mcp.CallToolResult, any, error) {
			a, err := s.actor(ctx, req)
			if err != nil {
				return nil, nil, err
			}
			out, err := h(ctx, a, in)
			return nil, out, err
		})
}

// ---------------------------------------------------------------- inputs

type showClocksIn struct {
	CampaignID uint `json:"campaign_id"`
}
type showChallengeIn struct {
	AgentID uint   `json:"agent_id"`
	Skill   string `json:"skill,omitempty" jsonschema:"preselect this skill"`
}

func (s *Server) addWidgetTools() {
	uiTool(s, "show_agent", "agent-sheet", "Show an Agent's sheet as an interactive widget the Seer can edit in place (harm, skills, XP, tracks, load, contacts, clocks). Use when the Seer asks to see or open a sheet; use get_agent when you only need the data.",
		func(ctx context.Context, a campaign.Actor, in agentIn) (any, error) {
			return s.sheetView(a, in.AgentID)
		})
	uiTool(s, "show_clocks", "clocks", "Show a campaign's clocks as a widget: tick them, reveal Seer-only ones to the players, or start a new one. Use when the Seer asks to see the clocks.",
		func(ctx context.Context, a campaign.Actor, in showClocksIn) (any, error) {
			return s.clocksView(a, in.CampaignID)
		})
	uiTool(s, "show_challenge", "challenge", "Open the challenge helper widget for one Agent: pick the skill and what's in play, see both hands' card counts, enter the cards played and resolve (pp. 15-19). Use when a challenge starts at the table.",
		func(ctx context.Context, a campaign.Actor, in showChallengeIn) (any, error) {
			return s.challengeView(a, in.AgentID, in.Skill)
		})
}

// ---------------------------------------------------------------- views

type viewSkill struct {
	Name   string `json:"name"`
	Points int    `json:"points"`
	Max    int    `json:"max"` // 3, or 4 once the 4th pip is unlocked
}

type viewSuit struct {
	Suit   string      `json:"suit"`
	XP     int         `json:"xp"`
	XPKey  string      `json:"xp_key"`
	Harm   []string    `json:"harm"` // always two boxes; "" is empty
	Skills []viewSkill `json:"skills"`
}

type viewAbility struct {
	Name          string `json:"name"`
	Text          string `json:"text"`
	SheetText     string `json:"sheet_text,omitempty"` // Character Sheets 1.3 wording where it differs
	Page          int    `json:"page,omitempty"`
	Source        string `json:"source,omitempty"`
	ClassOfOrigin string `json:"class_of_origin,omitempty"` // learned from another class
}

type sheet struct {
	Agent     *db.Agent      `json:"agent"`
	Class     map[string]any `json:"class,omitempty"`
	Player    string         `json:"player,omitempty"`
	Campaign  string         `json:"campaign,omitempty"`
	Suits     []viewSuit     `json:"suits"`
	Abilities []viewAbility  `json:"abilities"`
	Contacts  []db.Contact   `json:"contacts"`
	Clocks    []db.Clock     `json:"clocks"`
}

func (s *Server) suits(ag *db.Agent) ([]viewSuit, error) {
	snap, err := s.snapshot()
	if err != nil {
		return nil, err
	}
	xp := map[string]int{"Swords": ag.XPSwords, "Wands": ag.XPWands, "Cups": ag.XPCups, "Pentacles": ag.XPPentacles}
	var out []viewSuit
	for _, suit := range []string{"Swords", "Wands", "Cups", "Pentacles"} {
		row := viewSuit{Suit: suit, XP: xp[suit], XPKey: "xp_" + strings.ToLower(suit), Harm: []string{"", ""}}
		for _, sk := range snap.Skills.Skills {
			if sk.Suit == suit {
				max := 3
				if slices.Contains(ag.UnlockedFourth, sk.Name) {
					max = 4
				}
				row.Skills = append(row.Skills, viewSkill{Name: sk.Name, Points: ag.Skills[sk.Name], Max: max})
			}
		}
		for i, m := range ag.Harm[suit] {
			if i < 2 {
				row.Harm[i] = m
			}
		}
		out = append(out, row)
	}
	return out, nil
}

func (s *Server) playerName(ag *db.Agent) string {
	if ag.OwnerID == nil {
		return ""
	}
	var u db.User
	if s.db.First(&u, *ag.OwnerID).Error != nil {
		return ""
	}
	return u.Name
}

func (s *Server) sheetView(a campaign.Actor, id uint) (*sheet, error) {
	ag, err := s.svc.Agent(a, id)
	if err != nil {
		return nil, err
	}
	snap, err := s.snapshot()
	if err != nil {
		return nil, err
	}
	v := &sheet{Agent: ag, Player: s.playerName(ag), Abilities: []viewAbility{}, Clocks: []db.Clock{}}
	if v.Suits, err = s.suits(ag); err != nil {
		return nil, err
	}
	if c := snap.Class(ag.Class); c != nil {
		v.Class = map[string]any{"name": c.Name, "guild": c.Guild, "motto": c.Motto, "ability_xp_track": c.AbilityXPTrack}
	}
	for _, have := range ag.Abilities {
		ab := viewAbility{Name: have.Name, Text: have.Text, Source: have.Source}
		for _, cl := range snap.Classes.Classes {
			for _, x := range cl.Abilities {
				if have.ID != "" && x.ID == have.ID {
					ab.Name, ab.Text, ab.SheetText, ab.Page = x.Name, x.Text, x.SheetText, x.Page
					if cl.ID != ag.Class {
						ab.ClassOfOrigin = cl.Name
					}
				}
			}
		}
		v.Abilities = append(v.Abilities, ab)
	}
	if v.Contacts, err = s.svc.Contacts(a, id); err != nil {
		return nil, err
	}
	if ag.CampaignID != 0 {
		if c, err := s.svc.Campaign(a, ag.CampaignID); err == nil {
			v.Campaign = c.Name
		}
		var clocks []db.Clock
		_ = s.svc.List(a, "clock", ag.CampaignID, &clocks, "name")
		for _, c := range clocks {
			if c.AgentID != nil && *c.AgentID == id {
				v.Clocks = append(v.Clocks, c)
			}
		}
	}
	return v, nil
}

type clockRow struct {
	db.Clock
	AgentName string `json:"agent_name,omitempty"`
}

type clocksView struct {
	CampaignID uint       `json:"campaign_id"`
	Campaign   string     `json:"campaign"`
	Clocks     []clockRow `json:"clocks"`
	Segments   []int      `json:"segment_choices"` // p. 86
	Statuses   []string   `json:"statuses"`
}

func (s *Server) clocksView(a campaign.Actor, id uint) (*clocksView, error) {
	c, err := s.svc.Campaign(a, id)
	if err != nil {
		return nil, err
	}
	snap, err := s.snapshot()
	if err != nil {
		return nil, err
	}
	v := &clocksView{CampaignID: c.ID, Campaign: c.Name, Clocks: []clockRow{}, Segments: snap.Limits.Clock.Segments, Statuses: snap.Campaign.ClockStatus}
	var clocks []db.Clock
	if err := s.svc.List(a, "clock", id, &clocks, "status <> 'Running', name"); err != nil {
		return nil, err
	}
	agents, _ := s.svc.Agents(a, id)
	names := map[uint]string{}
	for _, ag := range agents {
		names[ag.ID] = ag.Name
	}
	for _, cl := range clocks {
		row := clockRow{Clock: cl}
		if cl.AgentID != nil {
			row.AgentName = names[*cl.AgentID]
		}
		v.Clocks = append(v.Clocks, row)
	}
	return v, nil
}

type challengeAgent struct {
	ID          uint     `json:"id"`
	Name        string   `json:"name"`
	Burden      string   `json:"burden,omitempty"`
	BurdenTrack int      `json:"burden_track"`
	Ideal       string   `json:"ideal,omitempty"`
	IdealTrack  int      `json:"ideal_track"`
	Vices       []string `json:"vices"`
	Virtues     []string `json:"virtues"`
}

type challengeWidget struct {
	Agent challengeAgent `json:"agent"`
	Suits []viewSuit     `json:"suits"`
	Skill string         `json:"skill,omitempty"`
	Pips  []string       `json:"pips"` // the 40 pips, for card pickers
	// Consequence menus (p. 17 failure, p. 18 complicated success).
	Ideas map[string][]string `json:"ideas"`
}

func (s *Server) challengeView(a campaign.Actor, id uint, skill string) (*challengeWidget, error) {
	ag, err := s.svc.Agent(a, id)
	if err != nil {
		return nil, err
	}
	snap, err := s.snapshot()
	if err != nil {
		return nil, err
	}
	if skill != "" && challenge.SuitOf(skill) == "" {
		return nil, fmt.Errorf("unknown skill %q", skill)
	}
	v := &challengeWidget{Skill: skill, Ideas: map[string][]string{"failure": challenge.FailureIdeas, "complicated": challenge.ComplicatedIdeas},
		Agent: challengeAgent{ID: ag.ID, Name: ag.Name, Burden: ag.Burden, BurdenTrack: ag.BurdenTrack, Ideal: ag.Ideal, IdealTrack: ag.IdealTrack,
			Vices: orEmpty(ag.Vices), Virtues: orEmpty(ag.Virtues)}}
	if v.Suits, err = s.suits(ag); err != nil {
		return nil, err
	}
	for _, c := range snap.Cards.Pips {
		v.Pips = append(v.Pips, c.Name)
	}
	return v, nil
}

func orEmpty(xs []string) []string {
	if xs == nil {
		return []string{}
	}
	return xs
}
