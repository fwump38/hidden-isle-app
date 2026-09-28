package campaign

import (
	"fmt"
	"slices"

	"gorm.io/gorm"

	"github.com/fwump38/hidden-isle-app/internal/db"
	"github.com/fwump38/hidden-isle-app/internal/gamedata"
)

// kind describes one type of campaign record for the generic create/update/delete/undo code.
type kind struct {
	name      string
	new       func() any
	campaign  func(obj any) uint
	display   func(obj any) string
	vis       func(obj any) (db.Visibility, *uint) // visibility of the record's events
	secret    []string                             // Seer-only fields: hidden from players, logged separately
	seerOnly  []string                             // fields players may not change
	seerWrite bool                                 // only the Seer may create, change or delete
	seerDel   bool                                 // only the Seer may delete
	canWrite  func(s *Service, tx *gorm.DB, a Actor, obj any) error
	canRead   func(a Actor, obj any) bool
	// access decides whether the actor may see the record at all. Default: campaign membership.
	access   func(s *Service, tx *gorm.DB, a Actor, obj any) error
	validate func(snap *gamedata.Snapshot, obj any) []string
	// checkPatch runs in Update, after the seerOnly check and before the patch is applied, with
	// obj still holding the record's current (pre-patch) values. It rejects changes checkWrite
	// and seerOnly can't express, such as the Agent sheet lock (see agent_lock.go).
	checkPatch func(s *Service, tx *gorm.DB, a Actor, obj any, patch Patch, o Opts) error
}

// Fields nobody may patch.
var lockedFields = []string{"id", "campaign_id", "created_at", "updated_at"}

var kinds = map[string]*kind{}

func register(k *kind) { kinds[k.name] = k }

func kindOf(name string) (*kind, error) {
	k, ok := kinds[name]
	if !ok {
		return nil, fmt.Errorf("unknown record type %q", name)
	}
	return k, nil
}

func party(any) (db.Visibility, *uint)   { return db.VisParty, nil }
func seerVis(any) (db.Visibility, *uint) { return db.VisSeer, nil }
func always(Actor, any) bool             { return true }
func seerOnlyRead(a Actor, _ any) bool   { return a.IsSeer() }

func init() {
	register(&kind{
		name: "campaign", new: func() any { return &db.Campaign{} },
		campaign: func(o any) uint { return o.(*db.Campaign).ID },
		display:  func(o any) string { return o.(*db.Campaign).Name },
		vis:      party, seerWrite: true, seerDel: true, canRead: always,
		validate: func(snap *gamedata.Snapshot, o any) []string {
			c := o.(*db.Campaign)
			var ps []string
			if c.Name == "" {
				ps = append(ps, "the campaign needs a name")
			}
			oneOf(&ps, "mode", c.Mode, snap.Campaign.CampaignMode)
			return ps
		},
	})

	register(&kind{
		name: "agent", new: func() any { return &db.Agent{} },
		campaign: func(o any) uint { return o.(*db.Agent).CampaignID },
		display:  func(o any) string { return o.(*db.Agent).Name },
		// An Agent outside any campaign is private to its player; in a campaign everyone there may read it (p. 28).
		vis: func(o any) (db.Visibility, *uint) {
			ag := o.(*db.Agent)
			if ag.CampaignID == 0 {
				return db.VisOwner, ag.OwnerID
			}
			return db.VisParty, nil
		},
		seerOnly: []string{"owner_id", "class", "locked_at"}, canRead: always,
		access: func(s *Service, tx *gorm.DB, a Actor, o any) error { return s.agentAccess(tx, a, o.(*db.Agent)) },
		canWrite: func(s *Service, tx *gorm.DB, a Actor, o any) error {
			if ownsAgent(a, o.(*db.Agent)) {
				return nil
			}
			return ErrForbidden
		},
		validate:   func(snap *gamedata.Snapshot, o any) []string { return validateAgent(snap, o.(*db.Agent)) },
		checkPatch: checkAgentPatch,
	})

	register(&kind{
		name: "contact", new: func() any { return &db.Contact{} },
		campaign: func(o any) uint { return o.(*db.Contact).CampaignID },
		display:  func(o any) string { return o.(*db.Contact).Name },
		vis: func(o any) (db.Visibility, *uint) {
			if o.(*db.Contact).CampaignID == 0 {
				return db.VisOwner, nil // set from the Agent in record()
			}
			return db.VisParty, nil
		},
		seerOnly: []string{"agent_id"}, canRead: always,
		access: func(s *Service, tx *gorm.DB, a Actor, o any) error {
			var ag db.Agent
			if err := tx.First(&ag, o.(*db.Contact).AgentID).Error; err != nil {
				return ErrNotFound
			}
			return s.agentAccess(tx, a, &ag)
		},
		canWrite: func(s *Service, tx *gorm.DB, a Actor, o any) error {
			c := o.(*db.Contact)
			var ag db.Agent
			if err := tx.First(&ag, c.AgentID).Error; err != nil {
				return ErrNotFound
			}
			c.CampaignID = ag.CampaignID // contacts follow their Agent
			if ownsAgent(a, &ag) {
				return nil
			}
			return ErrForbidden
		},
		validate: func(snap *gamedata.Snapshot, o any) []string {
			c := o.(*db.Contact)
			var ps []string
			if c.Name == "" {
				ps = append(ps, "the contact needs a name")
			}
			inRange(&ps, "affection", c.Affection, snap.Limits.Contact.Affection)
			inRange(&ps, "distance", c.Distance, snap.Limits.Contact.Distance)
			oneOf(&ps, "kind", c.Kind, snap.Campaign.ContactKind)
			return ps
		},
		checkPatch: checkContactPatch,
	})

	register(&kind{
		name: "clock", new: func() any { return &db.Clock{} },
		campaign: func(o any) uint { return o.(*db.Clock).CampaignID },
		display:  func(o any) string { return o.(*db.Clock).Name },
		vis: func(o any) (db.Visibility, *uint) {
			if o.(*db.Clock).Visibility == db.VisSeer {
				return db.VisSeer, nil
			}
			return db.VisParty, nil
		},
		seerOnly: []string{"agent_id", "visibility", "scope", "linked_to", "segments"},
		seerDel:  true,
		canRead:  func(a Actor, o any) bool { return a.IsSeer() || o.(*db.Clock).Visibility != db.VisSeer },
		// Players may tick clocks on their own Agent (ability and proficiency clocks).
		canWrite: func(s *Service, tx *gorm.DB, a Actor, o any) error {
			c := o.(*db.Clock)
			if a.IsSeer() {
				return nil
			}
			if c.AgentID != nil && c.Visibility != db.VisSeer {
				var ag db.Agent
				if tx.First(&ag, *c.AgentID).Error == nil && ag.OwnerID != nil && *ag.OwnerID == a.User.ID {
					return nil
				}
			}
			return ErrForbidden
		},
		validate: func(snap *gamedata.Snapshot, o any) []string {
			c := o.(*db.Clock)
			var ps []string
			if c.Name == "" {
				ps = append(ps, "name the clock for what happens when it fills (p. 86)")
			}
			if !slices.Contains(snap.Limits.Clock.Segments, c.Segments) {
				ps = append(ps, fmt.Sprintf("a clock has %v segments (%s)", snap.Limits.Clock.Segments, snap.Limits.Clock.Page))
			}
			if c.Filled < 0 || c.Filled > c.Segments {
				ps = append(ps, fmt.Sprintf("filled must be 0-%d", c.Segments))
			}
			oneOf(&ps, "scope", c.Scope, snap.Campaign.ClockScope)
			oneOf(&ps, "status", c.Status, snap.Campaign.ClockStatus)
			oneOf(&ps, "visibility", string(c.Visibility), []string{string(db.VisParty), string(db.VisSeer)})
			return ps
		},
	})

	register(&kind{
		name: "adversary", new: func() any { return &db.Adversary{} },
		campaign: func(o any) uint { return o.(*db.Adversary).CampaignID },
		display:  func(o any) string { return o.(*db.Adversary).Name },
		vis: func(o any) (db.Visibility, *uint) {
			if o.(*db.Adversary).Hidden {
				return db.VisSeer, nil
			}
			return db.VisParty, nil
		},
		secret: []string{"secrets"}, seerWrite: true, seerDel: true,
		canRead: func(a Actor, o any) bool { return a.IsSeer() || !o.(*db.Adversary).Hidden },
		validate: func(snap *gamedata.Snapshot, o any) []string {
			ad := o.(*db.Adversary)
			var ps []string
			if ad.Name == "" {
				ps = append(ps, "the adversary needs a name (a rumor is enough, p. 28)")
			}
			if ad.Progress < 0 || ad.TrackLength < 0 || (ad.TrackLength > 0 && ad.Progress > ad.TrackLength) {
				ps = append(ps, fmt.Sprintf("progress must be 0-%d (%s)", ad.TrackLength, snap.Limits.Adversary.Progress.Page))
			}
			oneOf(&ps, "status", ad.Status, snap.Campaign.AdversaryStatus)
			return ps
		},
	})

	register(&kind{
		name: "session", new: func() any { return &db.Session{} },
		campaign: func(o any) uint { return o.(*db.Session).CampaignID },
		display: func(o any) string {
			s := o.(*db.Session)
			return fmt.Sprintf("Session %d: %s", s.Number, s.Title)
		},
		vis: party, secret: []string{"prep", "divination", "next_time"}, seerWrite: true, seerDel: true, canRead: always,
		validate: func(snap *gamedata.Snapshot, o any) []string {
			s := o.(*db.Session)
			var ps []string
			if s.Title == "" {
				ps = append(ps, "the session needs a title")
			}
			oneOf(&ps, "status", s.Status, snap.Campaign.SessionStatus)
			if s.Adventure != "" {
				ok := false
				for _, a := range snap.Adventures.Adventures {
					ok = ok || a.ID == s.Adventure
				}
				if !ok {
					ps = append(ps, fmt.Sprintf("unknown adventure %q", s.Adventure))
				}
			}
			return ps
		},
	})

	register(&kind{
		name: "territory", new: func() any { return &db.Territory{} },
		campaign: func(o any) uint { return o.(*db.Territory).CampaignID },
		display:  func(o any) string { return o.(*db.Territory).Name },
		vis:      party, seerWrite: true, seerDel: true, canRead: always,
		validate: func(_ *gamedata.Snapshot, o any) []string {
			if o.(*db.Territory).Name == "" {
				return []string{"the territory needs a name"}
			}
			return nil
		},
	})

	register(&kind{
		name: "house_ruling", new: func() any { return &db.HouseRuling{} },
		campaign: func(o any) uint { return o.(*db.HouseRuling).CampaignID },
		display:  func(o any) string { return truncate(o.(*db.HouseRuling).Ruling, 40) },
		vis:      party, seerWrite: true, seerDel: true, canRead: always,
		validate: func(_ *gamedata.Snapshot, o any) []string {
			if o.(*db.HouseRuling).Ruling == "" {
				return []string{"write the ruling"}
			}
			return nil
		},
	})

	register(&kind{
		name: "seer_note", new: func() any { return &db.SeerNote{} },
		campaign: func(o any) uint { return o.(*db.SeerNote).CampaignID },
		display:  func(o any) string { return o.(*db.SeerNote).Title },
		vis:      seerVis, seerWrite: true, seerDel: true, canRead: seerOnlyRead,
		validate: func(_ *gamedata.Snapshot, o any) []string {
			if o.(*db.SeerNote).Title == "" {
				return []string{"the note needs a title"}
			}
			return nil
		},
	})
}

func ownsAgent(a Actor, ag *db.Agent) bool {
	return a.IsSeer() || (a.User != nil && ag.OwnerID != nil && *ag.OwnerID == a.User.ID)
}

// agentAccess: the Seer and the owner always; campaign members for an Agent in their campaign.
func (s *Service) agentAccess(tx *gorm.DB, a Actor, ag *db.Agent) error {
	if a.User == nil {
		return ErrForbidden
	}
	if ownsAgent(a, ag) {
		return nil
	}
	if ag.CampaignID == 0 {
		return ErrNotFound // someone else's private Agent: don't reveal it exists
	}
	return s.canView(tx, a, ag.CampaignID)
}

func validateAgent(snap *gamedata.Snapshot, a *db.Agent) []string {
	l := snap.Limits.Agent
	var ps []string
	if a.Name == "" {
		ps = append(ps, "the Agent needs a name")
	}
	if snap.Class(a.Class) == nil {
		ps = append(ps, fmt.Sprintf("unknown class %q", a.Class))
	}
	oneOf(&ps, "status", a.Status, snap.Campaign.AgentStatus)
	oneOf(&ps, "adulthood verb", a.AdultVerb, []string{"survived", "flourished"})
	inRange(&ps, "burden track", a.BurdenTrack, l.BurdenTrack)
	inRange(&ps, "ideal track", a.IdealTrack, l.IdealTrack)
	for label, v := range map[string]int{"Swords XP": a.XPSwords, "Wands XP": a.XPWands, "Cups XP": a.XPCups, "Pentacles XP": a.XPPentacles} {
		inRange(&ps, label, v, l.SuitXP)
	}
	inRange(&ps, "ability XP", a.XPAbility, l.AbilityXP)
	inRange(&ps, "load used", a.LoadUsed, l.LoadUsed)

	suitOf := map[string]string{}
	for _, sk := range snap.Skills.Skills {
		suitOf[sk.Name] = sk.Suit
	}
	for _, sk := range a.UnlockedFourth {
		if suitOf[sk] == "" {
			ps = append(ps, fmt.Sprintf("unknown skill %q in unlocked 4th pips", sk))
		}
	}
	for sk, v := range a.Skills {
		if suitOf[sk] == "" {
			ps = append(ps, fmt.Sprintf("unknown skill %q", sk))
			continue
		}
		max := l.Skill.Max
		if slices.Contains(a.UnlockedFourth, sk) {
			max = l.Skill.MaxUnlocked
		}
		if v < l.Skill.Min || v > max {
			ps = append(ps, fmt.Sprintf("%s must be %d-%d (%s; 4 only with the 4th pip unlocked)", sk, l.Skill.Min, max, l.Skill.Page))
		}
	}
	suits := map[string]bool{}
	for _, s := range suitOf {
		suits[s] = true
	}
	for suit, marks := range a.Harm {
		if !suits[suit] {
			ps = append(ps, fmt.Sprintf("unknown suit %q in harm", suit))
		}
		if len(marks) > l.HarmPerSuit.Max {
			ps = append(ps, fmt.Sprintf("%s can hold at most %d harm (%s)", suit, l.HarmPerSuit.Max, l.HarmPerSuit.Page))
		}
		for _, m := range marks {
			if !slices.Contains(l.HarmPerSuit.Types, m) {
				ps = append(ps, fmt.Sprintf("harm mark %q must be one of %v", m, l.HarmPerSuit.Types))
			}
		}
	}
	if len(a.Virtues) > l.Virtues.Max {
		ps = append(ps, fmt.Sprintf("at most %d virtues; move extras to fulfilled virtues (%s)", l.Virtues.Max, l.Virtues.Page))
	}
	known := map[string]bool{}
	for _, c := range snap.Classes.Classes {
		for _, ab := range c.Abilities {
			known[ab.ID] = true
		}
	}
	for _, ab := range a.Abilities {
		if ab.ID != "" && !known[ab.ID] {
			ps = append(ps, fmt.Sprintf("unknown ability %q", ab.ID))
		}
		if ab.ID == "" && ab.Name == "" {
			ps = append(ps, "a custom ability needs a name")
		}
	}
	for _, p := range a.Proficiencies {
		oneOf(&ps, "proficiency rank", p.Rank, l.ProficiencyClock.Ranks)
		if p.Segments < 0 || p.Segments > l.ProficiencyClock.Segments {
			ps = append(ps, fmt.Sprintf("%s clock must be 0-%d (%s)", p.School, l.ProficiencyClock.Segments, l.ProficiencyClock.Page))
		}
		if p.Boxes < 0 || p.Boxes > 3 {
			ps = append(ps, fmt.Sprintf("%s rank boxes must be 0-3", p.School))
		}
	}
	return ps
}
