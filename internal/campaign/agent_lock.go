package campaign

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/fwump38/hidden-isle-app/internal/db"
	"github.com/fwump38/hidden-isle-app/internal/gamedata"
)

// agentCreationLocked are the Agent fields fixed once the Agent is locked in (p. 40-41): built
// during character creation and, per the table's own house rule, not meant to change afterward
// except by the Seer. Age and look are deliberately not in this list: a player may want to update
// either over time.
var agentCreationLocked = []string{
	"name", "culture", "why",
	"child_phrase", "child_card", "adult_verb", "adult_phrase", "adult_card",
	"burden", "burden_card", "ideal", "ideal_card",
}

// checkAgentPatch enforces the sheet lock. Once an Agent is locked in, a player may no longer
// freely edit their creation-time choices, skill points or the abilities list — those now change
// only through the rules (SpendSuitXP, SpendAbilityXP, AddAbility, RemoveAbility all set
// Opts.system to make the matching Update call) or through the Seer, who this check exempts.
func checkAgentPatch(s *Service, tx *gorm.DB, a Actor, obj any, patch Patch, o Opts) error {
	ag := obj.(*db.Agent)
	if a.IsSeer() || ag.LockedAt == nil {
		return nil
	}
	for f := range patch {
		if slices.Contains(agentCreationLocked, f) {
			return fmt.Errorf("%s is locked in once the Seer locks the Agent in; ask the Seer to change it: %w", f, ErrForbidden)
		}
	}
	if o.system {
		return nil
	}
	if _, ok := patch["skills"]; ok {
		return fmt.Errorf("skill points are locked in; fill a suit's XP track and spend it for a point instead (p. 25): %w", ErrForbidden)
	}
	if _, ok := patch["abilities"]; ok {
		return fmt.Errorf("abilities are locked in; fill the ability XP track and spend it for a new one instead (p. 25): %w", ErrForbidden)
	}
	for _, f := range []string{"xp_swords", "xp_wands", "xp_cups", "xp_pentacles", "xp_ability"} {
		raw, ok := patch[f]
		if !ok {
			continue
		}
		var nv int
		if err := json.Unmarshal(raw, &nv); err != nil {
			return err
		}
		if nv < xpField(ag, f) {
			return fmt.Errorf("XP can only go up, until the track fills and resets: %w", ErrForbidden)
		}
	}
	return nil
}

func xpField(ag *db.Agent, name string) int {
	switch name {
	case "xp_swords":
		return ag.XPSwords
	case "xp_wands":
		return ag.XPWands
	case "xp_cups":
		return ag.XPCups
	case "xp_pentacles":
		return ag.XPPentacles
	case "xp_ability":
		return ag.XPAbility
	}
	return 0
}

// checkContactPatch locks a starting contact's identity (name, kind, land, description) once its
// Agent is locked in; affection and distance keep changing through play regardless (p. 66), and a
// contact made after the lock (a new downtime contact) isn't covered by it.
func checkContactPatch(s *Service, tx *gorm.DB, a Actor, obj any, patch Patch, o Opts) error {
	if a.IsSeer() {
		return nil
	}
	c := obj.(*db.Contact)
	var ag db.Agent
	if err := tx.First(&ag, c.AgentID).Error; err != nil || ag.LockedAt == nil || c.CreatedAt.After(*ag.LockedAt) {
		return nil
	}
	for _, f := range []string{"name", "kind", "land", "description"} {
		if _, ok := patch[f]; ok {
			return fmt.Errorf("%s is locked in for a starting contact; ask the Seer to change it: %w", f, ErrForbidden)
		}
	}
	return nil
}

// LockAgents locks in every Agent currently in the campaign that isn't locked yet: a button the
// Seer presses once the roster for a session is set (p. 40-41). From then on, that Agent's
// creation-time choices, skill points and abilities change only through the rules or the Seer. An
// Agent added to the campaign afterward stays freely editable until the Seer locks it in too.
func (s *Service) LockAgents(a Actor, campaignID uint, o Opts) (int, error) {
	if !a.IsSeer() {
		return 0, ErrForbidden
	}
	var agents []db.Agent
	if err := s.DB.Where("campaign_id = ? AND locked_at IS NULL", campaignID).Find(&agents).Error; err != nil {
		return 0, err
	}
	if o.Reason == "" {
		o.Reason = "locked in by the Seer (p. 40)"
	}
	o.system = true
	now := time.Now()
	for i, ag := range agents {
		if _, err := s.Update(a, "agent", ag.ID, Patch{"locked_at": raw(now)}, o); err != nil {
			return i, fmt.Errorf("%s: %w", ag.Name, err)
		}
	}
	return len(agents), nil
}

// abilityProficiencyGrants lists abilities that, when taken, fill segments of a named magical
// proficiency rather than starting their own clock (Master of Matter, Master of the Mind).
var abilityProficiencyGrants = map[string]struct {
	School   string
	Segments int
}{
	"master-of-matter":   {"Transmutation", 4},
	"master-of-the-mind": {"Mentalism", 4},
}

// abilityContactGrants lists abilities that, when taken, create a new contact (The Old Ways,
// Celestial Bargain). Kind must match the campaign's Contact Kind list.
var abilityContactGrants = map[string]struct {
	Kind         string
	MaxAffection bool // Celestial Bargain sets affection to maximum immediately; The Old Ways doesn't say a starting value
}{
	"the-old-ways":      {"Deity (The Old Ways)", false},
	"celestial-bargain": {"Angel or Demon (Celestial Bargain)", true},
}

// AbilityGrantsContact says whether taking abilityID creates a new contact (The Old Ways,
// Celestial Bargain), for the UI to prompt for its name rather than silently skip it.
func AbilityGrantsContact(abilityID string) bool {
	_, ok := abilityContactGrants[abilityID]
	return ok
}

func findAbility(snap *gamedata.Snapshot, id string) (*gamedata.Ability, *gamedata.Class) {
	for i, cl := range snap.Classes.Classes {
		for j, x := range cl.Abilities {
			if x.ID == id {
				return &snap.Classes.Classes[i].Abilities[j], &snap.Classes.Classes[i]
			}
		}
	}
	return nil, nil
}

// AddAbility gives agentID a class ability. By themselves an Agent learns only their own class's
// abilities; with a contact at maximum affection they may learn one from any class (p. 25). This
// is a free choice before the Agent is locked in (character creation, p. 41, or the Seer teaching
// one directly); once locked, a player reaches it only through SpendAbilityXP, which sets
// Opts.system. Some abilities carry a mechanical grant (p. 43-64): a clock, filled proficiency
// segments, or a new contact — contactName/contactDesc name it; leave contactName blank to skip
// and add that contact by hand later.
func (s *Service) AddAbility(a Actor, agentID uint, abilityID, contactName, contactDesc string, extra Patch, o Opts) (*db.Agent, error) {
	ag, err := s.Agent(a, agentID)
	if err != nil {
		return nil, err
	}
	if !s.CanEditAgent(a, ag) {
		return nil, ErrForbidden
	}
	if !a.IsSeer() && ag.LockedAt != nil && !o.system {
		return nil, fmt.Errorf("abilities are locked in; fill the ability XP track and spend it for a new one (p. 25): %w", ErrForbidden)
	}
	if slices.ContainsFunc(ag.Abilities, func(x db.AgentAbility) bool { return x.ID == abilityID }) {
		return ag, nil
	}
	snap, err := s.snap()
	if err != nil {
		return nil, err
	}
	found, fromClass := findAbility(snap, abilityID)
	if found == nil {
		return nil, fmt.Errorf("unknown ability %q", abilityID)
	}
	if fromClass.ID != ag.Class && !a.IsSeer() {
		cs, _ := s.Contacts(a, agentID)
		taught := slices.ContainsFunc(cs, func(c db.Contact) bool { return c.Affection >= snap.Limits.Contact.Affection.Max })
		if !taught {
			return nil, fmt.Errorf("%s belongs to the %s; only a contact at maximum affection can teach an ability from another class (p. 25): %w", found.Name, fromClass.Name, ErrForbidden)
		}
	}
	ability := db.AgentAbility{ID: abilityID, Source: "class"}
	patch := Patch{}
	for k, v := range extra {
		patch[k] = v
	}
	if found.Clock != nil && ag.CampaignID != 0 {
		cl := &db.Clock{CampaignID: ag.CampaignID, Name: found.Clock.Name, Segments: found.Clock.Segments,
			Scope: "Ability", LinkedTo: found.Name, AgentID: &ag.ID, Status: "Running", Visibility: db.VisParty}
		if err := s.Create(a, "clock", cl, Opts{Reason: fmt.Sprintf("granted by %s (p. 43)", found.Name)}); err != nil {
			return nil, fmt.Errorf("granting %s's clock: %w", found.Name, err)
		}
		ability.GrantedClockID = &cl.ID
	}
	if g, ok := abilityProficiencyGrants[abilityID]; ok {
		profs := slices.Clone(ag.Proficiencies)
		i := slices.IndexFunc(profs, func(p db.AgentProficiency) bool { return p.School == g.School })
		if i < 0 {
			profs = append(profs, db.AgentProficiency{School: g.School, Rank: "Novice", Segments: g.Segments})
		} else if profs[i].Segments < g.Segments {
			profs[i].Segments = g.Segments
		}
		patch["proficiencies"] = raw(profs)
		ability.GrantedProficiencySchool, ability.GrantedProficiencySegments = g.School, g.Segments
	}
	if g, ok := abilityContactGrants[abilityID]; ok && strings.TrimSpace(contactName) != "" {
		aff := 0
		if g.MaxAffection {
			aff = snap.Limits.Contact.Affection.Max
		}
		ct := &db.Contact{AgentID: ag.ID, CampaignID: ag.CampaignID, Name: strings.TrimSpace(contactName),
			Kind: g.Kind, Description: strings.TrimSpace(contactDesc), Affection: aff}
		if err := s.Create(a, "contact", ct, Opts{Reason: fmt.Sprintf("granted by %s (p. 43)", found.Name)}); err != nil {
			return nil, fmt.Errorf("granting %s's contact: %w", found.Name, err)
		}
		ability.GrantedContactID = &ct.ID
	}
	patch["abilities"] = raw(append(slices.Clone(ag.Abilities), ability))
	if o.Reason == "" {
		o.Reason = fmt.Sprintf("learned %s (p. 25)", found.Name)
	}
	o.system = true
	out, err := s.Update(a, "agent", agentID, patch, o)
	if err != nil {
		return nil, err
	}
	return out.(*db.Agent), nil
}

// AddCustomAbility adds a ritual, a contact-taught or a house-ruled ability that isn't in the
// rules data. Same lock as AddAbility; no mechanical grants (there's nothing to look up).
func (s *Service) AddCustomAbility(a Actor, agentID uint, name, text, source string, o Opts) (*db.Agent, error) {
	ag, err := s.Agent(a, agentID)
	if err != nil {
		return nil, err
	}
	if !s.CanEditAgent(a, ag) {
		return nil, ErrForbidden
	}
	if !a.IsSeer() && ag.LockedAt != nil {
		return nil, fmt.Errorf("abilities are locked in; ask the Seer to add it: %w", ErrForbidden)
	}
	if strings.TrimSpace(name) == "" {
		return nil, errors.New("name the ability")
	}
	ability := db.AgentAbility{Name: strings.TrimSpace(name), Text: strings.TrimSpace(text), Source: orDefault(source, "other")}
	if o.Reason == "" {
		o.Reason = fmt.Sprintf("added %s", ability.Name)
	}
	o.system = true
	out, err := s.Update(a, "agent", agentID, Patch{"abilities": raw(append(slices.Clone(ag.Abilities), ability))}, o)
	if err != nil {
		return nil, err
	}
	return out.(*db.Agent), nil
}

func orDefault(s, def string) string {
	if strings.TrimSpace(s) == "" {
		return def
	}
	return s
}

// RemoveAbility takes an ability off the sheet: freely before the Agent is locked in (to fix a
// mistake — the rules have no way to lose an ability otherwise), Seer-only after. Reverses
// whatever it granted: deletes its clock or contact, and rolls back the proficiency segments it
// filled (never below what's already there for another reason).
func (s *Service) RemoveAbility(a Actor, agentID uint, index int, o Opts) (*db.Agent, error) {
	ag, err := s.Agent(a, agentID)
	if err != nil {
		return nil, err
	}
	if !s.CanEditAgent(a, ag) {
		return nil, ErrForbidden
	}
	if !a.IsSeer() && ag.LockedAt != nil {
		return nil, fmt.Errorf("abilities are locked in; only the Seer can remove one now: %w", ErrForbidden)
	}
	if index < 0 || index >= len(ag.Abilities) {
		return nil, fmt.Errorf("no ability %d", index)
	}
	removed := ag.Abilities[index]
	if removed.GrantedClockID != nil {
		// Deleting a clock is normally Seer-only (kind "clock" has seerDel: true); this one
		// exists only because of the ability being removed, by the same actor who added it, so
		// it goes straight to the DB rather than through the generic (Seer-only) Delete.
		if err := s.DB.Delete(&db.Clock{}, *removed.GrantedClockID).Error; err != nil {
			return nil, err
		}
	}
	if removed.GrantedContactID != nil {
		if err := s.Delete(a, "contact", *removed.GrantedContactID, Opts{Reason: "its ability was removed"}); err != nil && !errors.Is(err, ErrNotFound) {
			return nil, err
		}
	}
	patch := Patch{}
	if removed.GrantedProficiencySchool != "" {
		profs := slices.Clone(ag.Proficiencies)
		if i := slices.IndexFunc(profs, func(p db.AgentProficiency) bool { return p.School == removed.GrantedProficiencySchool }); i >= 0 {
			profs[i].Segments = max(0, profs[i].Segments-removed.GrantedProficiencySegments)
			if profs[i].Segments == 0 && profs[i].Boxes == 0 {
				profs = slices.Delete(profs, i, i+1)
			}
			patch["proficiencies"] = raw(profs)
		}
	}
	patch["abilities"] = raw(slices.Delete(slices.Clone(ag.Abilities), index, index+1))
	if o.Reason == "" {
		o.Reason = "removed an ability"
	}
	o.system = true
	out, err := s.Update(a, "agent", agentID, patch, o)
	if err != nil {
		return nil, err
	}
	return out.(*db.Agent), nil
}

// GrantAbilityContact names the contact an already-taken ability grants (The Old Ways, Celestial
// Bargain), when it wasn't named when the ability was added. Same lock as AddAbility: free before
// the Agent is locked in, Seer-only after.
func (s *Service) GrantAbilityContact(a Actor, agentID uint, index int, name, desc string, o Opts) (*db.Agent, error) {
	ag, err := s.Agent(a, agentID)
	if err != nil {
		return nil, err
	}
	if !s.CanEditAgent(a, ag) {
		return nil, ErrForbidden
	}
	if !a.IsSeer() && ag.LockedAt != nil {
		return nil, fmt.Errorf("abilities are locked in; ask the Seer to add the contact: %w", ErrForbidden)
	}
	if index < 0 || index >= len(ag.Abilities) {
		return nil, fmt.Errorf("no ability %d", index)
	}
	ability := ag.Abilities[index]
	g, ok := abilityContactGrants[ability.ID]
	if !ok {
		return nil, fmt.Errorf("that ability doesn't grant a contact")
	}
	if ability.GrantedContactID != nil {
		return ag, nil
	}
	if strings.TrimSpace(name) == "" {
		return nil, errors.New("name the contact")
	}
	snap, err := s.snap()
	if err != nil {
		return nil, err
	}
	aff := 0
	if g.MaxAffection {
		aff = snap.Limits.Contact.Affection.Max
	}
	ct := &db.Contact{AgentID: ag.ID, CampaignID: ag.CampaignID, Name: strings.TrimSpace(name),
		Kind: g.Kind, Description: strings.TrimSpace(desc), Affection: aff}
	abName := ability.ID
	if found, _ := findAbility(snap, ability.ID); found != nil {
		abName = found.Name
	}
	if err := s.Create(a, "contact", ct, Opts{Reason: fmt.Sprintf("granted by %s (p. 43)", abName)}); err != nil {
		return nil, fmt.Errorf("granting %s's contact: %w", abName, err)
	}
	abilities := slices.Clone(ag.Abilities)
	abilities[index].GrantedContactID = &ct.ID
	if o.Reason == "" {
		o.Reason = fmt.Sprintf("named %s's contact", abName)
	}
	o.system = true
	out, err := s.Update(a, "agent", agentID, Patch{"abilities": raw(abilities)}, o)
	if err != nil {
		return nil, err
	}
	return out.(*db.Agent), nil
}

// SpendSuitXP redeems a full suit XP track (p. 25): +1 point in a skill of that suit (max 3, or 4
// once its 4th pip is unlocked), and the track resets to 0. Only the Seer may do this with the
// track short of full.
func (s *Service) SpendSuitXP(a Actor, agentID uint, skillName string, o Opts) (*db.Agent, error) {
	ag, err := s.Agent(a, agentID)
	if err != nil {
		return nil, err
	}
	if !s.CanEditAgent(a, ag) {
		return nil, ErrForbidden
	}
	snap, err := s.snap()
	if err != nil {
		return nil, err
	}
	var suit string
	for _, sk := range snap.Skills.Skills {
		if sk.Name == skillName {
			suit = sk.Suit
		}
	}
	if suit == "" {
		return nil, fmt.Errorf("unknown skill %q", skillName)
	}
	cur := map[string]int{"Swords": ag.XPSwords, "Wands": ag.XPWands, "Cups": ag.XPCups, "Pentacles": ag.XPPentacles}[suit]
	xpMax := snap.Limits.Agent.SuitXP.Max
	if !a.IsSeer() && cur < xpMax {
		return nil, fmt.Errorf("the %s XP track isn't full yet (%d/%d): %w", suit, cur, xpMax, ErrForbidden)
	}
	skillMax := snap.Limits.Agent.Skill.Max
	if slices.Contains(ag.UnlockedFourth, skillName) {
		skillMax = snap.Limits.Agent.Skill.MaxUnlocked
	}
	if ag.Skills[skillName] >= skillMax {
		return nil, fmt.Errorf("%s is already at its maximum (%d): %w", skillName, skillMax, ErrForbidden)
	}
	skills := make(map[string]int, len(ag.Skills))
	for k, v := range ag.Skills {
		skills[k] = v
	}
	skills[skillName]++
	if o.Reason == "" {
		o.Reason = fmt.Sprintf("%s XP track filled (p. 25)", suit)
	}
	o.system = true
	out, err := s.Update(a, "agent", agentID, Patch{"skills": raw(skills), "xp_" + strings.ToLower(suit): raw(0)}, o)
	if err != nil {
		return nil, err
	}
	return out.(*db.Agent), nil
}

// SpendAbilityXP redeems a full ability XP track (p. 25): learn a new ability and the track
// resets to 0. Only the Seer may do this with the track short of full.
func (s *Service) SpendAbilityXP(a Actor, agentID uint, abilityID, contactName, contactDesc string, o Opts) (*db.Agent, error) {
	ag, err := s.Agent(a, agentID)
	if err != nil {
		return nil, err
	}
	snap, err := s.snap()
	if err != nil {
		return nil, err
	}
	if !a.IsSeer() && ag.XPAbility < snap.Limits.Agent.AbilityXP.Max {
		return nil, fmt.Errorf("the ability XP track isn't full yet (%d/%d): %w", ag.XPAbility, snap.Limits.Agent.AbilityXP.Max, ErrForbidden)
	}
	if o.Reason == "" {
		o.Reason = "ability XP track filled (p. 25)"
	}
	o.system = true
	return s.AddAbility(a, agentID, abilityID, contactName, contactDesc, Patch{"xp_ability": raw(0)}, o)
}
