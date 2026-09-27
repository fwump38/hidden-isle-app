package mcpsrv

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/fwump38/hidden-isle-app/internal/campaign"
	"github.com/fwump38/hidden-isle-app/internal/cards"
	"github.com/fwump38/hidden-isle-app/internal/db"
	"github.com/fwump38/hidden-isle-app/internal/gamedata"
)

const instructions = `The Hidden Isle: a tarot RPG set in 1562 (Forged in the Dark). This server is the campaign's master copy (Agents, contacts, clocks, adversaries, territories, sessions, journals, the change log) and serves the rules text.

Standing rules:
- Cite pages from the rules text ("p." = Rulebook 1.4, "Sheet p." = Character Sheets 1.3, "Ref p." = Reference Sheets). Quote ability text exactly. Never invent numbers or rules; if the text doesn't cover something, say so and offer the Seer a clearly labeled suggested ruling.
- The table draws real tarot cards: ask what was drawn. Use draw_cards only when asked for a digital draw.
- Every change needs a short reason; it goes in the change log that players can read. Seer-only material (session prep, adversary secrets, Seer notes, hidden clocks and adversaries, adventure text) must never appear in anything players see.
- Long-form writing (session logs, recaps, histories) is written by people. write_entry stores what the Seer or a player wrote, lightly tidied if asked; don't invent events that aren't in their notes.
- get_campaign first when starting play; it has the campaign's current state.`

type empty struct{}

// Fields is a set of record fields by their JSON name, e.g. {"burden_track": 3}.
type Fields map[string]any

func (f Fields) patch() (campaign.Patch, error) {
	p := campaign.Patch{}
	for k, v := range f {
		b, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		p[k] = b
	}
	return p, nil
}

type items struct {
	Items any `json:"items"`
}

type note struct {
	Result any    `json:"result,omitempty"`
	Note   string `json:"note,omitempty"`
}

// tool registers a handler that receives the Seer as actor.
func tool[In any](s *Server, name, desc string, h func(ctx context.Context, a campaign.Actor, in In) (any, error)) {
	mcp.AddTool(s.mcp, &mcp.Tool{Name: name, Description: desc},
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

type campaignIn struct {
	CampaignID uint `json:"campaign_id"`
}
type agentIn struct {
	AgentID uint `json:"agent_id"`
}
type reasonField struct {
	Reason string `json:"reason" jsonschema:"why, for the change log (players can read it)"`
}

type listRecordsIn struct {
	CampaignID uint   `json:"campaign_id"`
	Kind       string `json:"kind" jsonschema:"clock, adversary, territory, session, house_ruling or seer_note"`
}
type getRecordIn struct {
	Kind string `json:"kind" jsonschema:"campaign, agent, contact, clock, adversary, territory, session, house_ruling or seer_note"`
	ID   uint   `json:"id"`
}
type createRecordIn struct {
	CampaignID uint   `json:"campaign_id"`
	Kind       string `json:"kind" jsonschema:"clock, adversary, territory, session, house_ruling, seer_note or contact"`
	Fields     Fields `json:"fields" jsonschema:"field values by name; a contact needs agent_id"`
	reasonField
}
type updateRecordIn struct {
	Kind   string `json:"kind" jsonschema:"campaign, agent, contact, clock, adversary, territory, session, house_ruling or seer_note"`
	ID     uint   `json:"id"`
	Fields Fields `json:"fields" jsonschema:"fields to replace, by name (maps and lists are replaced whole)"`
	reasonField
	Override bool `json:"override,omitempty" jsonschema:"break a rules limit on purpose (recorded as a house ruling); needs a reason"`
}
type deleteRecordIn struct {
	Kind string `json:"kind"`
	ID   uint   `json:"id"`
	reasonField
}
type createCampaignIn struct {
	Name     string `json:"name"`
	Mode     string `json:"mode,omitempty" jsonschema:"group (default), solitaire or Seer-less"`
	Merciful bool   `json:"merciful,omitempty" jsonschema:"Merciful Mode (pp. 93-94)"`
}
type createAgentIn struct {
	CampaignID uint   `json:"campaign_id,omitempty" jsonschema:"0 or omitted: not in a campaign"`
	Name       string `json:"name"`
	Class      string `json:"class" jsonschema:"class id or name, e.g. prowler"`
	Player     string `json:"player,omitempty" jsonschema:"the player's name; omit for an Agent the Seer runs"`
}
type moveAgentIn struct {
	AgentID    uint `json:"agent_id"`
	CampaignID uint `json:"campaign_id" jsonschema:"0 takes it out of any campaign"`
	reasonField
}
type harmIn struct {
	AgentID uint   `json:"agent_id"`
	Suit    string `json:"suit" jsonschema:"Swords, Wands, Cups or Pentacles"`
	Type    string `json:"type" jsonschema:"P (physical), S (spiritual) or T (trauma)"`
	Amount  int    `json:"amount,omitempty" jsonschema:"default 1"`
	reasonField
}
type healIn struct {
	AgentID uint   `json:"agent_id"`
	Type    string `json:"type" jsonschema:"P, S, T or any (any never removes trauma)"`
	Amount  int    `json:"amount"`
	Suit    string `json:"suit,omitempty" jsonschema:"limit to one suit"`
	reasonField
}
type xpIn struct {
	AgentID uint   `json:"agent_id"`
	Track   string `json:"track" jsonschema:"swords, wands, cups, pentacles or ability"`
	Amount  int    `json:"amount"`
	reasonField
}
type tickIn struct {
	ClockID uint `json:"clock_id"`
	Amount  int  `json:"amount" jsonschema:"segments to fill (negative empties)"`
	reasonField
}
type advanceIn struct {
	AdversaryID uint   `json:"adversary_id"`
	Step        string `json:"step" jsonschema:"steady (+1), rapid (+2) or setback (-1)"`
	reasonField
}
type logIn struct {
	CampaignID uint   `json:"campaign_id"`
	EntityType string `json:"entity_type,omitempty"`
	EntityID   uint   `json:"entity_id,omitempty"`
	Limit      int    `json:"limit,omitempty"`
}
type undoIn struct {
	EventID uint `json:"event_id" jsonschema:"from get_log"`
	reasonField
}
type entriesIn struct {
	CampaignID uint   `json:"campaign_id"`
	Kind       string `json:"kind,omitempty" jsonschema:"session_log, recap, history, journal or note"`
}
type writeEntryIn struct {
	CampaignID uint   `json:"campaign_id"`
	ID         uint   `json:"id,omitempty" jsonschema:"to edit an existing entry"`
	Kind       string `json:"kind" jsonschema:"session_log, recap, history (needs agent_id) or note"`
	Title      string `json:"title"`
	Body       string `json:"body" jsonschema:"what a person wrote (lightly tidied if asked); never invented events"`
	Visibility string `json:"visibility" jsonschema:"party (everyone, once published), seer (Seer only) or owner"`
	Published  bool   `json:"published,omitempty"`
	SessionID  uint   `json:"session_id,omitempty"`
	AgentID    uint   `json:"agent_id,omitempty"`
}
type searchIn struct {
	Query             string `json:"query"`
	IncludeAdventures bool   `json:"include_adventures,omitempty" jsonschema:"also search the published adventures (Seer-only spoilers)"`
	Limit             int    `json:"limit,omitempty"`
}
type readIn struct {
	Ref string `json:"ref" jsonschema:"a ref from search_rules, or a file path like condensed/rules/01_core_rules.md"`
}
type classIn struct {
	Class string `json:"class" jsonschema:"class id or name, e.g. hunter or Hunter"`
}
type cardIn struct {
	Name string `json:"name" jsonschema:"a vision card, e.g. The Tower, XVI, Queen of Cups"`
}
type tableIn struct {
	Name string `json:"name" jsonschema:"limits, oracle, downtime, setting or skills"`
}
type drawIn struct {
	Deck  string          `json:"deck" jsonschema:"vision (22 Majors + 16 Courts) or pips (Ace-10 in four suits)"`
	Hands []cards.Request `json:"hands" jsonschema:"one entry per hand, e.g. [{name: agent, count: 3}, {name: seer, count: 4}]"`
}

func opts(r reasonField) campaign.Opts { return campaign.Opts{Reason: r.Reason} }

// ---------------------------------------------------------------- tools

func (s *Server) addTools() {
	tool(s, "whoami", "Who this connection is, the app build and the rules-data version. Use it to check the connection.",
		func(ctx context.Context, a campaign.Actor, _ empty) (any, error) {
			out := map[string]string{"name": a.User.Name, "role": string(a.User.Role), "build": s.build}
			if snap := s.data.Current(); snap != nil {
				out["game_data"] = snap.Label()
			}
			return out, nil
		})

	// ---- campaign state
	tool(s, "list_campaigns", "All campaigns (id, name, mode, season).",
		func(ctx context.Context, a campaign.Actor, _ empty) (any, error) {
			cs, err := s.svc.Campaigns(a)
			return items{cs}, err
		})
	tool(s, "get_campaign", "A campaign's current state: settings, players, Agents (summary), running clocks, adversaries, territories, recent sessions, house rulings. Start here.",
		func(ctx context.Context, a campaign.Actor, in campaignIn) (any, error) {
			return s.campaignState(a, in.CampaignID)
		})
	tool(s, "get_agent", "One Agent's full sheet, with its class abilities quoted exactly (and page), contacts and clocks.",
		func(ctx context.Context, a campaign.Actor, in agentIn) (any, error) {
			return s.agentState(a, in.AgentID)
		})
	tool(s, "list_records", "The records of one kind in a campaign: clock, adversary, territory, session, house_ruling or seer_note (Seer-only).",
		func(ctx context.Context, a campaign.Actor, in listRecordsIn) (any, error) {
			var list any
			switch in.Kind {
			case "clock":
				list = &[]db.Clock{}
			case "adversary":
				list = &[]db.Adversary{}
			case "territory":
				list = &[]db.Territory{}
			case "session":
				list = &[]db.Session{}
			case "house_ruling":
				list = &[]db.HouseRuling{}
			case "seer_note":
				list = &[]db.SeerNote{}
			default:
				return nil, fmt.Errorf("kind must be clock, adversary, territory, session, house_ruling or seer_note")
			}
			return items{list}, s.svc.List(a, in.Kind, in.CampaignID, list, "id")
		})
	tool(s, "get_record", "One record by kind and id.",
		func(ctx context.Context, a campaign.Actor, in getRecordIn) (any, error) {
			return s.svc.Get(a, in.Kind, in.ID)
		})
	tool(s, "get_log", "The change log (newest first): who changed what, from what, to what, and why. Filter by entity_type/entity_id. Event ids are what undo_change takes.",
		func(ctx context.Context, a campaign.Actor, in logIn) (any, error) {
			evs, err := s.svc.Events(a, in.CampaignID, campaign.EventFilter{EntityType: in.EntityType, EntityID: in.EntityID, Limit: in.Limit})
			type ev struct {
				ID       uint   `json:"id"`
				When     string `json:"when"`
				Who      string `json:"who"`
				Summary  string `json:"summary"`
				Reason   string `json:"reason,omitempty"`
				SeerOnly bool   `json:"seer_only,omitempty"`
				Undone   bool   `json:"undone,omitempty"`
			}
			var out []ev
			for _, e := range evs {
				out = append(out, ev{e.ID, e.CreatedAt.Format("2006-01-02 15:04"), e.ActorName, e.Summary, e.Reason, e.Visibility == db.VisSeer, e.RevertedBy != nil})
			}
			return items{out}, err
		})
	tool(s, "list_entries", "Written entries in a campaign (session logs, recaps, histories, journals shared with the Seer).",
		func(ctx context.Context, a campaign.Actor, in entriesIn) (any, error) {
			es, err := s.svc.Entries(a, in.CampaignID, in.Kind, 0)
			return items{es}, err
		})

	// ---- changes
	tool(s, "create_campaign", "Start a campaign (with the six printed territories).",
		func(ctx context.Context, a campaign.Actor, in createCampaignIn) (any, error) {
			c := &db.Campaign{Name: in.Name, Mode: in.Mode, Merciful: in.Merciful}
			return c, s.svc.CreateCampaign(a, c, campaign.Opts{Reason: "new campaign"})
		})
	tool(s, "create_agent", "Create an Agent sheet with the class's pre-filled skills (p. 40). Naming a player makes it theirs and adds them to the campaign. Fill in the rest with update_record.",
		func(ctx context.Context, a campaign.Actor, in createAgentIn) (any, error) {
			var owner *uint
			if in.Player != "" {
				var u db.User
				if err := s.db.Where("LOWER(name) = LOWER(?) AND active = ?", in.Player, true).First(&u).Error; err != nil {
					return nil, fmt.Errorf("no active player named %q", in.Player)
				}
				owner = &u.ID
			}
			return s.svc.NewAgent(a, in.CampaignID, in.Name, strings.ToLower(in.Class), owner, campaign.Opts{})
		})
	tool(s, "move_agent", "Move an Agent into a campaign (adds its player) or out of any campaign (campaign_id 0).",
		func(ctx context.Context, a campaign.Actor, in moveAgentIn) (any, error) {
			return note{Note: "moved"}, s.svc.AssignAgent(a, in.AgentID, in.CampaignID, opts(in.reasonField))
		})
	tool(s, "create_record", "Create a clock, adversary, territory, session, house_ruling, seer_note or contact. Clock fields: name (what happens when it fills), segments (3/4/6/8), filled, scope, visibility (party/seer), agent_id. Adversary: name, leader, plot, motivation, members, track_length, status, hidden, secrets (Seer-only). Session: title, date (YYYY-MM-DD), adventure, mission_type, territory, summary (player-safe), prep/divination/next_time (Seer-only). Contact: agent_id, name, kind, card, land, description, affection, distance.",
		func(ctx context.Context, a campaign.Actor, in createRecordIn) (any, error) {
			p, err := in.Fields.patch()
			if err != nil {
				return nil, err
			}
			return s.svc.CreateRecord(a, in.Kind, in.CampaignID, p, opts(in.reasonField))
		})
	tool(s, "update_record", "Change fields of any record, validated against the rules' limits and logged. Agent fields include name, status, burden, burden_card, burden_track, ideal, ideal_card, ideal_track, vices, virtues, fulfilled_virtues (lists), skills (map skill→points; send the whole map), unlocked_fourth, harm (map suit→[P/S/T]; send the whole map), xp_swords/xp_wands/xp_cups/xp_pentacles/xp_ability, abilities ([{id}] or custom [{name,text,source}]), proficiencies ([{school,rank,boxes,segments}]), magical_sources, items, load_used, age, culture, look, why, child_phrase, child_card, adult_verb, adult_phrase, adult_card, notes. Prefer add_harm/heal/award_xp/tick_clock/advance_adversary for those.",
		func(ctx context.Context, a campaign.Actor, in updateRecordIn) (any, error) {
			p, err := in.Fields.patch()
			if err != nil {
				return nil, err
			}
			o := opts(in.reasonField)
			o.Override = in.Override
			return s.svc.Update(a, in.Kind, in.ID, p, o)
		})
	tool(s, "delete_record", "Delete a record (restorable with undo_change, except campaigns: delete those from the web UI).",
		func(ctx context.Context, a campaign.Actor, in deleteRecordIn) (any, error) {
			if in.Kind == "campaign" {
				return nil, fmt.Errorf("delete campaigns from the web UI's settings page")
			}
			return note{Note: "deleted; undo_change can restore it"}, s.svc.Delete(a, in.Kind, in.ID, opts(in.reasonField))
		})
	tool(s, "add_harm", "Mark harm on an Agent (p. 23): fills empty boxes in the suit; trauma can upgrade an existing mark (p. 24). If the suit is full, the error names suits with room: ask the player where it goes.",
		func(ctx context.Context, a campaign.Actor, in harmIn) (any, error) {
			ag, err := s.svc.AddHarm(a, in.AgentID, in.Suit, in.Type, in.Amount, opts(in.reasonField))
			if err != nil {
				return nil, err
			}
			return note{Result: map[string]any{"harm": ag.Harm}, Note: "2 harm in a suit: 1 fewer card with that suit's skills (p. 23)."}, nil
		})
	tool(s, "heal", "Remove harm from an Agent: P or S (any removes either, never trauma); trauma only with type T (heart-to-heart or a virtue, p. 24).",
		func(ctx context.Context, a campaign.Actor, in healIn) (any, error) {
			ag, n, err := s.svc.Heal(a, in.AgentID, in.Type, in.Amount, in.Suit, opts(in.reasonField))
			if err != nil {
				return nil, err
			}
			return note{Result: map[string]any{"harm": ag.Harm, "healed": n}}, nil
		})
	tool(s, "award_xp", "Add XP to a suit track or the ability track (max 7). A full track's advance is the player's choice (p. 25): the note says what to do next.",
		func(ctx context.Context, a campaign.Actor, in xpIn) (any, error) {
			_, n, err := s.svc.AwardXP(a, in.AgentID, in.Track, in.Amount, opts(in.reasonField))
			return note{Note: n}, err
		})
	tool(s, "tick_clock", "Fill (or empty, with a negative amount) clock segments.",
		func(ctx context.Context, a campaign.Actor, in tickIn) (any, error) {
			_, n, err := s.svc.TickClock(a, in.ClockID, in.Amount, opts(in.reasonField))
			return note{Note: n}, err
		})
	tool(s, "advance_adversary", "Advance an adversary's progress track at wrap-up (p. 81): steady +1, rapid +2, setback -1.",
		func(ctx context.Context, a campaign.Actor, in advanceIn) (any, error) {
			_, n, err := s.svc.AdvanceAdversary(a, in.AdversaryID, in.Step, opts(in.reasonField))
			return note{Note: n}, err
		})
	tool(s, "drift_contacts", "Downtime step 2 for one Agent (p. 66): +1 distance on every contact; full distance resets to 0 and costs 1 affection. Handles the Old Ways and Celestial Bargain exceptions.",
		func(ctx context.Context, a campaign.Actor, in agentIn) (any, error) {
			n, err := s.svc.DriftContacts(a, in.AgentID, campaign.Opts{})
			return items{n}, err
		})
	tool(s, "undo_change", "Undo one change from the log (fails if the same fields changed again since; undo the later change first).",
		func(ctx context.Context, a campaign.Actor, in undoIn) (any, error) {
			return note{Note: "undone"}, s.svc.Revert(a, in.EventID, in.Reason)
		})
	tool(s, "write_entry", "Save a session log, recap, history line or note that a person wrote. Recaps for players must be player-safe (no Seer-only material) and need published=true and visibility=party to be seen.",
		func(ctx context.Context, a campaign.Actor, in writeEntryIn) (any, error) {
			e := &db.Entry{ID: in.ID, CampaignID: in.CampaignID, Kind: in.Kind, Title: in.Title, Body: in.Body,
				Visibility: db.Visibility(in.Visibility), Published: in.Published}
			if in.SessionID != 0 {
				e.SessionID = &in.SessionID
			}
			if in.AgentID != 0 {
				e.AgentID = &in.AgentID
			}
			return e, s.svc.WriteEntry(a, e)
		})

	// ---- rules
	tool(s, "search_rules", "Search the rules text (condensed and full editions) for sections, with page cites. include_adventures also searches the published adventures: spoilers, Seer's eyes only.",
		func(ctx context.Context, a campaign.Actor, in searchIn) (any, error) {
			if s.rules == nil {
				return nil, fmt.Errorf("rules search isn't available")
			}
			hits, err := s.rules.Search(in.Query, in.IncludeAdventures, in.Limit)
			return items{hits}, err
		})
	tool(s, "read_rules", "Read a rules section by the ref from search_rules, or a whole file by path (e.g. condensed/rules/03_cycle_of_play.md).",
		func(ctx context.Context, a campaign.Actor, in readIn) (any, error) {
			if s.rules == nil {
				return nil, fmt.Errorf("rules search isn't available")
			}
			secs, err := s.rules.Read(in.Ref, true)
			return items{secs}, err
		})
	tool(s, "get_class", "A class with its motto, pre-filled skills, items and every ability. Ability text is verbatim from Rulebook 1.4 (with page); sheet_text is the Character Sheets 1.3 wording where it differs. Quote abilities exactly.",
		func(ctx context.Context, a campaign.Actor, in classIn) (any, error) {
			snap, err := s.snapshot()
			if err != nil {
				return nil, err
			}
			c := snap.Class(strings.ToLower(in.Class))
			if c == nil {
				c = snap.Class(in.Class)
			}
			if c == nil {
				return nil, fmt.Errorf("no class %q", in.Class)
			}
			return c, nil
		})
	tool(s, "lookup_card", "A vision card's Vision Guide entry (meaning, characters, locations, groups, bad outcomes), its Character History phrases and its burdens/ideals.",
		func(ctx context.Context, a campaign.Actor, in cardIn) (any, error) {
			snap, err := s.snapshot()
			if err != nil {
				return nil, err
			}
			q := strings.ToLower(strings.TrimSpace(in.Name))
			for _, c := range snap.Cards.Vision {
				if strings.ToLower(c.Name) == q || strings.ToLower(c.ID) == q || strings.ToLower(c.Numeral) == q ||
					strings.TrimPrefix(strings.ToLower(c.Name), "the ") == strings.TrimPrefix(q, "the ") {
					return c, nil
				}
			}
			return nil, fmt.Errorf("no vision card %q (the pips deck has no Vision Guide entries)", in.Name)
		})
	tool(s, "get_table", "Structured rules tables: limits (every numeric limit with its page), oracle (mission types, fate questions, random events, NPC methods), downtime (actions and contact bonuses), setting (mascots, names by region, magic schools and sources), skills.",
		func(ctx context.Context, a campaign.Actor, in tableIn) (any, error) {
			snap, err := s.snapshot()
			if err != nil {
				return nil, err
			}
			switch in.Name {
			case "limits":
				return snap.Limits, nil
			case "skills":
				return snap.Skills, nil
			}
			if t, ok := snap.Raw[in.Name]; ok {
				return t, nil
			}
			return nil, fmt.Errorf("table must be limits, oracle, downtime, setting or skills")
		})
	tool(s, "draw_cards", "Digital card draw, only when the Seer asks for one (the table normally draws real cards). Hands come from one shuffled deck, so no card is in two hands. Ace = 11 in challenges, 1 for fate numbers.",
		func(ctx context.Context, a campaign.Actor, in drawIn) (any, error) {
			snap, err := s.snapshot()
			if err != nil {
				return nil, err
			}
			hands, err := cards.Draw(snap, in.Deck, in.Hands)
			return note{Result: map[string]any{"hands": hands}, Note: "The Seer picks the Seer's card; each player picks their own."}, err
		})
}

// ---------------------------------------------------------------- composite reads

func (s *Server) campaignState(a campaign.Actor, id uint) (any, error) {
	c, err := s.svc.Campaign(a, id)
	if err != nil {
		return nil, err
	}
	out := map[string]any{"campaign": c}
	players, _ := s.svc.Members(a, id)
	var names []string
	for _, p := range players {
		names = append(names, p.Name)
	}
	out["players"] = names
	agents, _ := s.svc.Agents(a, id)
	type agentSummary struct {
		ID      uint                `json:"id"`
		Name    string              `json:"name"`
		Class   string              `json:"class"`
		Status  string              `json:"status"`
		Player  string              `json:"player,omitempty"`
		Harm    map[string][]string `json:"harm,omitempty"`
		Load    int                 `json:"load_used"`
		Burden  string              `json:"burden,omitempty"`
		Ideal   string              `json:"ideal,omitempty"`
		Summary string              `json:"tracks"`
	}
	var as []agentSummary
	for _, ag := range agents {
		sm := agentSummary{ID: ag.ID, Name: ag.Name, Class: ag.Class, Status: ag.Status, Harm: ag.Harm, Load: ag.LoadUsed, Burden: ag.Burden, Ideal: ag.Ideal,
			Summary: fmt.Sprintf("burden %d/7, ideal %d/7, XP swords %d wands %d cups %d pentacles %d ability %d", ag.BurdenTrack, ag.IdealTrack, ag.XPSwords, ag.XPWands, ag.XPCups, ag.XPPentacles, ag.XPAbility)}
		if ag.OwnerID != nil {
			var u db.User
			if s.db.First(&u, *ag.OwnerID).Error == nil {
				sm.Player = u.Name
			}
		}
		as = append(as, sm)
	}
	out["agents"] = as
	var clocks []db.Clock
	_ = s.svc.List(a, "clock", id, &clocks, "name")
	var running []db.Clock
	for _, cl := range clocks {
		if cl.Status == "Running" {
			running = append(running, cl)
		}
	}
	out["running_clocks"] = running
	var advs []db.Adversary
	_ = s.svc.List(a, "adversary", id, &advs, "major desc, name")
	out["adversaries"] = advs
	var terr []db.Territory
	_ = s.svc.List(a, "territory", id, &terr, "id")
	out["territories"] = terr
	var sessions []db.Session
	_ = s.svc.List(a, "session", id, &sessions, "number desc")
	if len(sessions) > 5 {
		sessions = sessions[:5]
	}
	out["recent_sessions"] = sessions
	var rulings []db.HouseRuling
	_ = s.svc.List(a, "house_ruling", id, &rulings, "created_at")
	out["house_rulings"] = rulings
	return out, nil
}

func (s *Server) agentState(a campaign.Actor, id uint) (any, error) {
	ag, err := s.svc.Agent(a, id)
	if err != nil {
		return nil, err
	}
	out := map[string]any{"agent": ag}
	if snap := s.data.Current(); snap != nil {
		if c := snap.Class(ag.Class); c != nil {
			out["class"] = map[string]any{"name": c.Name, "guild": c.Guild, "motto": c.Motto, "ability_xp_track": c.AbilityXPTrack}
		}
		var abilities []gamedata.Ability
		for _, have := range ag.Abilities {
			for _, c := range snap.Classes.Classes {
				for _, x := range c.Abilities {
					if x.ID == have.ID {
						abilities = append(abilities, x)
					}
				}
			}
		}
		out["ability_text"] = abilities
	}
	out["contacts"], _ = s.svc.Contacts(a, id)
	if ag.CampaignID != 0 {
		var clocks []db.Clock
		_ = s.svc.List(a, "clock", ag.CampaignID, &clocks, "name")
		var mine []db.Clock
		for _, c := range clocks {
			if c.AgentID != nil && *c.AgentID == id {
				mine = append(mine, c)
			}
		}
		out["clocks"] = mine
	}
	return out, nil
}
