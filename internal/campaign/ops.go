package campaign

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/fwump38/hidden-isle-app/internal/db"
)

// Table-level operations used by MCP (and available to the web UI). Each is a read, a change
// in memory and one Update, so it gets the same permission checks, validation and change log.

// Kinds lists the record types that the generic create/update/delete operations accept.
var Kinds = []string{"campaign", "agent", "contact", "clock", "adversary", "territory", "session", "house_ruling", "seer_note"}

// NewRecord returns an empty record of kind with its defaults, for campaign cid.
func NewRecord(kind string, cid uint) (any, error) {
	switch kind {
	case "clock":
		return &db.Clock{CampaignID: cid, Status: "Running", Visibility: db.VisParty}, nil
	case "adversary":
		return &db.Adversary{CampaignID: cid, Status: "Rumored"}, nil
	case "territory":
		return &db.Territory{CampaignID: cid}, nil
	case "session":
		return &db.Session{CampaignID: cid, Status: "Prep"}, nil
	case "seer_note":
		return &db.SeerNote{CampaignID: cid}, nil
	case "house_ruling":
		return &db.HouseRuling{CampaignID: cid}, nil
	case "contact":
		return &db.Contact{CampaignID: cid}, nil
	}
	return nil, fmt.Errorf("can't create a %s this way (campaigns and Agents have their own operations)", kind)
}

// CreateRecord creates a record of kind in campaign cid from fields (JSON names, as in Update).
// A contact needs agent_id; its campaign follows the Agent. Sessions are numbered automatically.
func (s *Service) CreateRecord(a Actor, kind string, cid uint, fields Patch, o Opts) (any, error) {
	obj, err := NewRecord(kind, cid)
	if err != nil {
		return nil, err
	}
	for f := range fields {
		if slices.Contains(lockedFields, f) {
			delete(fields, f)
		}
	}
	if err := applyPatch(obj, fields); err != nil {
		return nil, err
	}
	switch r := obj.(type) {
	case *db.Contact:
		ag, err := s.Agent(a, r.AgentID)
		if err != nil {
			return nil, fmt.Errorf("contact needs a valid agent_id: %w", err)
		}
		r.CampaignID = ag.CampaignID
	case *db.Session:
		if r.Number == 0 {
			var n int64
			s.DB.Model(&db.Session{}).Where("campaign_id = ?", cid).Count(&n)
			r.Number = int(n) + 1
		}
	}
	if err := s.Create(a, kind, obj, o); err != nil {
		return nil, err
	}
	return obj, nil
}

func raw(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

var suits = []string{"Swords", "Wands", "Cups", "Pentacles"}

func suitName(s string) (string, error) {
	for _, x := range suits {
		if strings.EqualFold(x, strings.TrimSpace(s)) {
			return x, nil
		}
	}
	return "", fmt.Errorf("suit must be one of %s", strings.Join(suits, ", "))
}

// AddHarm marks `amount` harm of type P, S or T in a suit (p. 23): into empty boxes first; trauma
// may also upgrade an existing P or S (p. 24). A full suit is an error that names the suits with
// room, because the player then chooses where it goes.
func (s *Service) AddHarm(a Actor, agentID uint, suit, typ string, amount int, o Opts) (*db.Agent, error) {
	ag, err := s.Agent(a, agentID)
	if err != nil {
		return nil, err
	}
	suit, err = suitName(suit)
	if err != nil {
		return nil, err
	}
	typ = strings.ToUpper(strings.TrimSpace(typ))
	if typ != "P" && typ != "S" && typ != "T" {
		return nil, errors.New("type must be P (physical), S (spiritual) or T (trauma)")
	}
	if amount < 1 {
		amount = 1
	}
	harm := map[string][]string{}
	for k, v := range ag.Harm {
		harm[k] = slices.Clone(v)
	}
	for i := 0; i < amount; i++ {
		marks := harm[suit]
		switch {
		case len(marks) < 2:
			harm[suit] = append(marks, typ)
		case typ == "T" && slices.IndexFunc(marks, func(m string) bool { return m != "T" }) >= 0:
			marks[slices.IndexFunc(marks, func(m string) bool { return m != "T" })] = "T"
		default:
			var free []string
			for _, x := range suits {
				if len(harm[x]) < 2 {
					free = append(free, x)
				}
			}
			return nil, fmt.Errorf("%s is full after %d of %d harm; the player chooses another suit (room in: %s) (p. 23)", suit, i, amount, strings.Join(free, ", "))
		}
	}
	out, err := s.Update(a, "agent", agentID, Patch{"harm": raw(harm)}, o)
	if err != nil {
		return nil, err
	}
	return out.(*db.Agent), nil
}

// Heal removes up to `amount` harm of type P or S ("any" removes either), in one suit or across
// all suits. Trauma only heals with typ "T" (a heart-to-heart or a virtue, p. 24).
func (s *Service) Heal(a Actor, agentID uint, typ string, amount int, suit string, o Opts) (*db.Agent, int, error) {
	ag, err := s.Agent(a, agentID)
	if err != nil {
		return nil, 0, err
	}
	typ = strings.ToUpper(strings.TrimSpace(typ))
	if typ != "P" && typ != "S" && typ != "T" && typ != "ANY" {
		return nil, 0, errors.New("type must be P, S, T or any")
	}
	only := suits
	if suit != "" {
		sn, err := suitName(suit)
		if err != nil {
			return nil, 0, err
		}
		only = []string{sn}
	}
	harm := map[string][]string{}
	for k, v := range ag.Harm {
		harm[k] = slices.Clone(v)
	}
	healed := 0
	for _, x := range only {
		kept := harm[x][:0:0]
		for _, m := range harm[x] {
			match := m == typ || (typ == "ANY" && m != "T")
			if match && healed < amount {
				healed++
				continue
			}
			kept = append(kept, m)
		}
		harm[x] = kept
	}
	if healed == 0 {
		return ag, 0, nil
	}
	out, err := s.Update(a, "agent", agentID, Patch{"harm": raw(harm)}, o)
	if err != nil {
		return nil, 0, err
	}
	return out.(*db.Agent), healed, nil
}

// AwardXP adds XP to a suit track or the ability track, up to the track's maximum. It reports
// when the track is full; the advance (a skill point or a new ability, p. 25) is the player's
// choice, so it's left for a follow-up change, as is any XP beyond the maximum.
func (s *Service) AwardXP(a Actor, agentID uint, track string, amount int, o Opts) (*db.Agent, string, error) {
	ag, err := s.Agent(a, agentID)
	if err != nil {
		return nil, "", err
	}
	snap, err := s.snap()
	if err != nil {
		return nil, "", err
	}
	fields := map[string]*int{"swords": &ag.XPSwords, "wands": &ag.XPWands, "cups": &ag.XPCups, "pentacles": &ag.XPPentacles, "ability": &ag.XPAbility}
	key := strings.ToLower(strings.TrimSpace(track))
	p, ok := fields[key]
	if !ok {
		return nil, "", errors.New("track must be swords, wands, cups, pentacles or ability")
	}
	max := snap.Limits.Agent.SuitXP.Max
	if key == "ability" {
		max = snap.Limits.Agent.AbilityXP.Max
	}
	total := *p + amount
	newVal, leftover := total, 0
	if total > max {
		newVal, leftover = max, total-max
	}
	if newVal < 0 {
		newVal = 0
	}
	name := "xp_" + key
	out, err := s.Update(a, "agent", agentID, Patch{name: raw(newVal)}, o)
	if err != nil {
		return nil, "", err
	}
	note := fmt.Sprintf("%s XP %d → %d/%d.", key, *p, newVal, max)
	if newVal == max {
		if key == "ability" {
			note += " Track full: the player learns a new ability (from their class, or any class a max-affection contact could teach), then the track resets (p. 25)."
		} else {
			note += fmt.Sprintf(" Track full: the player gains 1 skill point in a %s skill of their choice (max 3, or 4 if unlocked), then the track resets (p. 25).", strings.ToUpper(key[:1])+key[1:])
		}
	}
	if leftover > 0 {
		note += fmt.Sprintf(" %d XP didn't fit; the Seer decides whether it carries over after the reset.", leftover)
	}
	return out.(*db.Agent), note, nil
}

// TickClock fills (or, with a negative amount, empties) segments, clamped to the clock.
func (s *Service) TickClock(a Actor, clockID uint, amount int, o Opts) (*db.Clock, string, error) {
	obj, err := s.Get(a, "clock", clockID)
	if err != nil {
		return nil, "", err
	}
	c := obj.(*db.Clock)
	n := c.Filled + amount
	n = max(0, min(n, c.Segments))
	out, err := s.Update(a, "clock", clockID, Patch{"filled": raw(n)}, o)
	if err != nil {
		return nil, "", err
	}
	note := fmt.Sprintf("%s: %d/%d.", c.Name, n, c.Segments)
	if n == c.Segments {
		note += " The clock is full: what it names happens now (p. 86). Ability clocks apply their effect and empty (p. 43)."
	}
	return out.(*db.Clock), note, nil
}

// AdvanceAdversary moves an adversary's progress: steady +1, rapid +2, setback -1 (p. 81).
func (s *Service) AdvanceAdversary(a Actor, id uint, step string, o Opts) (*db.Adversary, string, error) {
	obj, err := s.Get(a, "adversary", id)
	if err != nil {
		return nil, "", err
	}
	ad := obj.(*db.Adversary)
	delta, ok := map[string]int{"steady": 1, "rapid": 2, "setback": -1}[strings.ToLower(step)]
	if !ok {
		return nil, "", errors.New("step must be steady (+1), rapid (+2) or setback (-1) (p. 81)")
	}
	if ad.Hidden {
		return nil, "", errors.New("a progress track can't advance until the adversary is on the sheet (p. 28): unhide it first")
	}
	n := max(0, ad.Progress+delta)
	if ad.TrackLength > 0 {
		n = min(n, ad.TrackLength)
	}
	out, err := s.Update(a, "adversary", id, Patch{"progress": raw(n)}, o)
	if err != nil {
		return nil, "", err
	}
	note := fmt.Sprintf("%s: progress %d → %d", ad.Name, ad.Progress, n)
	if ad.TrackLength > 0 {
		note += fmt.Sprintf("/%d. The track has three sections: clearing the first reveals their identity, the middle starts the final phase, a full track puts the plan in motion (p. 28). Check the section marks on the printed sheet.", ad.TrackLength)
		if n == ad.TrackLength {
			note += " The track is full: the plan is in motion."
		}
	} else {
		note += ". Set track_length from the printed Adversary Sheet (Ref p. 8)."
	}
	return out.(*db.Adversary), note, nil
}

// DriftContacts is the downtime step "contacts drift away" (p. 66): +1 distance on each of an
// Agent's contacts; at full distance it resets to 0 and affection drops by 1. Deities (The Old
// Ways) can't gain distance, and angels or demons (Celestial Bargain) don't drift in downtime.
func (s *Service) DriftContacts(a Actor, agentID uint, o Opts) ([]string, error) {
	cs, err := s.Contacts(a, agentID)
	if err != nil {
		return nil, err
	}
	snap, err := s.snap()
	if err != nil {
		return nil, err
	}
	maxD := snap.Limits.Contact.Distance.Max
	if o.Reason == "" {
		o.Reason = "downtime: contacts drift (p. 66)"
	}
	var notes []string
	for _, c := range cs {
		if strings.HasPrefix(c.Kind, "Deity") || strings.HasPrefix(c.Kind, "Angel or Demon") {
			notes = append(notes, fmt.Sprintf("%s: no drift (%s)", c.Name, c.Kind))
			continue
		}
		d, aff := c.Distance+1, c.Affection
		if d >= maxD {
			d, aff = 0, max(0, aff-1)
		}
		if _, err := s.Update(a, "contact", c.ID, Patch{"distance": raw(d), "affection": raw(aff)}, o); err != nil {
			return notes, fmt.Errorf("%s: %w", c.Name, err)
		}
		n := fmt.Sprintf("%s: distance %d → %d", c.Name, c.Distance, d)
		if aff != c.Affection {
			n += fmt.Sprintf(", affection %d → %d", c.Affection, aff)
			if aff == 0 {
				n += " (at 0 affection the contact may be removed)"
			}
		}
		if c.Kind == "Fellow Agent" {
			n += " (fellow Agents clear distance when they share a mission or downtime action)"
		}
		notes = append(notes, n)
	}
	return notes, nil
}
