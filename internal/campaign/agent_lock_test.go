package campaign

import (
	"testing"

	"github.com/fwump38/hidden-isle-app/internal/db"
)

// TestLockAgents locks every Agent currently in the campaign, and only those.
func TestLockAgents(t *testing.T) {
	w := setup(t)
	if _, err := w.s.LockAgents(w.ana, w.camp.ID, Opts{}); !IsForbidden(err) {
		t.Errorf("a player locked agents: %v", err)
	}
	n, err := w.s.LockAgents(w.seer, w.camp.ID, Opts{})
	must(t, err)
	if n != 2 {
		t.Errorf("locked %d Agents, want 2", n)
	}
	ana, _ := w.s.Agent(w.seer, w.anaAgent.ID)
	if ana.LockedAt == nil {
		t.Error("Ana's Agent should be locked in")
	}
	// An Agent joining afterward isn't locked until the Seer locks in again.
	late, err := w.s.NewAgent(w.bram, w.camp.ID, "Later", "hunter", nil, Opts{})
	must(t, err)
	if late.LockedAt != nil {
		t.Error("a newly joined Agent shouldn't be locked in yet")
	}
	n, err = w.s.LockAgents(w.seer, w.camp.ID, Opts{})
	must(t, err)
	if n != 1 {
		t.Errorf("second lock-in caught %d Agents, want 1 (just the new one)", n)
	}
}

// TestLockedFieldsAreFixed: once locked, a player can't change creation-time fields, skill
// points or abilities directly; the Seer still can.
func TestLockedFieldsAreFixed(t *testing.T) {
	w := setup(t)
	id := w.anaAgent.ID
	lockNow(t, w)

	if _, err := w.s.Update(w.ana, "agent", id, patch(t, map[string]any{"name": "New Name"}), Opts{}); !IsForbidden(err) {
		t.Errorf("player changed a locked field: %v", err)
	}
	if _, err := w.s.Update(w.ana, "agent", id, patch(t, map[string]any{"skills": map[string]int{"Skirmish": 3}}), Opts{}); !IsForbidden(err) {
		t.Errorf("player changed skills directly once locked: %v", err)
	}
	if _, err := w.s.Update(w.ana, "agent", id, patch(t, map[string]any{"abilities": []db.AgentAbility{{ID: "burglar"}}}), Opts{}); !IsForbidden(err) {
		t.Errorf("player changed abilities directly once locked: %v", err)
	}
	// The Seer may still change any of it.
	if _, err := w.s.Update(w.seer, "agent", id, patch(t, map[string]any{"name": "Renamed"}), Opts{}); err != nil {
		t.Errorf("Seer should be able to rename a locked Agent: %v", err)
	}
	// Age and look were deliberately left off the lock list.
	if _, err := w.s.Update(w.ana, "agent", id, patch(t, map[string]any{"age": "30"}), Opts{}); err != nil {
		t.Errorf("age should stay editable after lock: %v", err)
	}
}

// TestXPOnlyGoesUpOnceLocked: a player can still add XP once locked, but not remove it; the
// Seer can do either.
func TestXPOnlyGoesUpOnceLocked(t *testing.T) {
	w := setup(t)
	id := w.anaAgent.ID
	lockNow(t, w)
	if _, err := w.s.Update(w.ana, "agent", id, patch(t, map[string]any{"xp_swords": 2}), Opts{}); err != nil {
		t.Errorf("increasing XP should still work once locked: %v", err)
	}
	if _, err := w.s.Update(w.ana, "agent", id, patch(t, map[string]any{"xp_swords": 1}), Opts{}); !IsForbidden(err) {
		t.Errorf("a player lowered XP once locked: %v", err)
	}
	if _, err := w.s.Update(w.seer, "agent", id, patch(t, map[string]any{"xp_swords": 0}), Opts{}); err != nil {
		t.Errorf("the Seer should be able to lower XP: %v", err)
	}
}

// TestSpendSuitXP redeems a full track for a skill point, atomically, and only when full.
func TestSpendSuitXP(t *testing.T) {
	w := setup(t)
	id := w.anaAgent.ID
	if _, err := w.s.SpendSuitXP(w.ana, id, "Skirmish", Opts{}); !IsForbidden(err) {
		t.Errorf("spent a suit track that wasn't full: %v", err)
	}
	_, err := w.s.Update(w.ana, "agent", id, patch(t, map[string]any{"xp_swords": 7}), Opts{})
	must(t, err)
	before, _ := w.s.Agent(w.ana, id)
	ag, err2 := w.s.SpendSuitXP(w.ana, id, "Skirmish", Opts{})
	must(t, err2)
	if ag.XPSwords != 0 {
		t.Errorf("xp_swords = %d, want 0 after spending", ag.XPSwords)
	}
	if ag.Skills["Skirmish"] != before.Skills["Skirmish"]+1 {
		t.Errorf("Skirmish = %d, want %d", ag.Skills["Skirmish"], before.Skills["Skirmish"]+1)
	}
	// Now locked: this must be the only way skill points move.
	lockNow(t, w)
	_, err3 := w.s.Update(w.ana, "agent", id, patch(t, map[string]any{"xp_swords": 7}), Opts{})
	must(t, err3)
	if _, err := w.s.SpendSuitXP(w.ana, id, "Skirmish", Opts{}); err != nil {
		t.Errorf("spending once locked should still work: %v", err)
	}
}

// TestAddAbilityGrantsAndClassRestriction covers the clock an ability grants, that only the
// Agent's own class is offered without a max-affection contact, and lock-gating.
func TestAddAbilityGrantsAndClassRestriction(t *testing.T) {
	w := setup(t)
	id := w.anaAgent.ID // hunter

	// Own class: fine, and its clock is created (Butcher: 4 segments, p. 49).
	ag, err := w.s.AddAbility(w.ana, id, "butcher", "", "", nil, Opts{})
	must(t, err)
	if len(ag.Abilities) != 1 || ag.Abilities[0].GrantedClockID == nil {
		t.Fatalf("abilities = %+v, want a granted clock", ag.Abilities)
	}
	var cl db.Clock
	must(t, w.s.DB.First(&cl, *ag.Abilities[0].GrantedClockID).Error)
	if cl.Segments != 4 || cl.Scope != "Ability" || cl.AgentID == nil || *cl.AgentID != id {
		t.Errorf("granted clock = %+v", cl)
	}

	// Another class's ability: forbidden without a max-affection contact.
	if _, err := w.s.AddAbility(w.ana, id, "burglar", "", "", nil, Opts{}); !IsForbidden(err) {
		t.Errorf("added another class's ability with no teaching contact: %v", err)
	}
	ct := &db.Contact{AgentID: id, CampaignID: w.camp.ID, Name: "Old Marco", Kind: "Fellow Agent", Affection: 6}
	must(t, w.s.Create(w.ana, "contact", ct, Opts{}))
	ag, err = w.s.AddAbility(w.ana, id, "burglar", "", "", nil, Opts{})
	must(t, err)
	if !containsAbility(ag.Abilities, "burglar") {
		t.Error("a max-affection contact should allow another class's ability")
	}

	// A contact-granting ability: named, it creates the contact.
	_, errc := w.s.Update(w.ana, "agent", id, patch(t, map[string]any{"class": "prowler"}), Opts{allowClass: true})
	must(t, errc)
	ag, err = w.s.AddAbility(w.ana, id, "the-old-ways", "Old Verminus", "a rat god", nil, Opts{})
	must(t, err)
	i := abilityIndex(ag.Abilities, "the-old-ways")
	if i < 0 || ag.Abilities[i].GrantedContactID == nil {
		t.Fatalf("abilities = %+v, want a granted contact", ag.Abilities)
	}
	var granted db.Contact
	must(t, w.s.DB.First(&granted, *ag.Abilities[i].GrantedContactID).Error)
	if granted.Name != "Old Verminus" || granted.Kind != "Deity (The Old Ways)" {
		t.Errorf("granted contact = %+v", granted)
	}

	// Locked: a player can no longer add one directly.
	lockNow(t, w)
	if _, err := w.s.AddAbility(w.ana, id, "master-of-matter", "", "", nil, Opts{}); !IsForbidden(err) {
		t.Errorf("player added an ability directly once locked: %v", err)
	}
	// The Seer still can.
	if _, err := w.s.AddAbility(w.seer, id, "master-of-matter", "", "", nil, Opts{}); err != nil {
		t.Errorf("Seer should still be able to add one: %v", err)
	}
}

// TestAddAbilityProficiencyGrant covers filling a named proficiency's segments (Master of
// Matter, p. 43) instead of a standalone clock.
func TestAddAbilityProficiencyGrant(t *testing.T) {
	w := setup(t)
	id := w.anaAgent.ID
	ag, err := w.s.AddAbility(w.ana, id, "master-of-matter", "", "", nil, Opts{})
	must(t, err)
	if len(ag.Proficiencies) != 1 || ag.Proficiencies[0].School != "Transmutation" || ag.Proficiencies[0].Segments != 4 {
		t.Errorf("proficiencies = %+v", ag.Proficiencies)
	}
}

// TestRemoveAbilityReversesGrants: removing an ability (pre-lock only, for a player) deletes its
// clock/contact and rolls back exactly the proficiency segments it filled.
func TestRemoveAbilityReversesGrants(t *testing.T) {
	w := setup(t)
	id := w.anaAgent.ID
	ag, err := w.s.AddAbility(w.ana, id, "butcher", "", "", nil, Opts{})
	must(t, err)
	clockID := *ag.Abilities[0].GrantedClockID

	ag, err = w.s.AddAbility(w.ana, id, "master-of-matter", "", "", nil, Opts{})
	must(t, err)
	// Hand-add extra proficiency segments from another source: removal must not eat those.
	profs := append([]db.AgentProficiency(nil), ag.Proficiencies...)
	profs[0].Segments = 6
	_, errp := w.s.Update(w.ana, "agent", id, patch(t, map[string]any{"proficiencies": profs}), Opts{})
	must(t, errp)

	ag, err = w.s.RemoveAbility(w.ana, id, 0, Opts{}) // removes butcher (index 0)
	must(t, err)
	if len(ag.Abilities) != 1 || ag.Abilities[0].ID != "master-of-matter" {
		t.Errorf("abilities after removing butcher = %+v", ag.Abilities)
	}
	if err := w.s.DB.First(&db.Clock{}, clockID).Error; err == nil {
		t.Error("butcher's clock should have been deleted")
	}

	ag, err = w.s.RemoveAbility(w.ana, id, 0, Opts{}) // removes master-of-matter
	must(t, err)
	if len(ag.Proficiencies) != 1 || ag.Proficiencies[0].Segments != 2 {
		t.Errorf("proficiency after removal = %+v, want segments 2 (6 - 4 granted)", ag.Proficiencies)
	}

	// Once locked, only the Seer may remove one.
	ag, err = w.s.AddAbility(w.ana, id, "butcher", "", "", nil, Opts{})
	must(t, err)
	_ = ag
	lockNow(t, w)
	if _, err := w.s.RemoveAbility(w.ana, id, 0, Opts{}); !IsForbidden(err) {
		t.Errorf("player removed an ability once locked: %v", err)
	}
	if _, err := w.s.RemoveAbility(w.seer, id, 0, Opts{}); err != nil {
		t.Errorf("Seer should still be able to remove one: %v", err)
	}
}

// TestSpendAbilityXP redeems a full ability track for a new ability, only when full.
func TestSpendAbilityXP(t *testing.T) {
	w := setup(t)
	id := w.anaAgent.ID
	if _, err := w.s.SpendAbilityXP(w.ana, id, "butcher", "", "", Opts{}); !IsForbidden(err) {
		t.Errorf("spent an ability track that wasn't full: %v", err)
	}
	_, erra := w.s.Update(w.ana, "agent", id, patch(t, map[string]any{"xp_ability": 7}), Opts{})
	must(t, erra)
	ag, err := w.s.SpendAbilityXP(w.ana, id, "butcher", "", "", Opts{})
	must(t, err)
	if ag.XPAbility != 0 || !containsAbility(ag.Abilities, "butcher") {
		t.Errorf("after spending: xp_ability=%d abilities=%+v", ag.XPAbility, ag.Abilities)
	}
}

// TestAssignAgentRules: only an Active Agent can join a campaign, and once locked in only the
// Seer can move it out.
func TestAssignAgentRules(t *testing.T) {
	w := setup(t)
	resting, err := w.s.NewAgent(w.ana, 0, "Resting One", "hunter", nil, Opts{})
	must(t, err)
	_, errs := w.s.Update(w.ana, "agent", resting.ID, patch(t, map[string]any{"status": "Dead"}), Opts{})
	must(t, errs)
	if err := w.s.AssignAgent(w.ana, resting.ID, w.camp.ID, Opts{}); !IsForbidden(err) {
		t.Errorf("a Dead Agent joined a campaign: %v", err)
	}
	if err := w.s.AssignAgent(w.seer, resting.ID, w.camp.ID, Opts{}); err != nil {
		t.Errorf("the Seer should be able to move a non-Active Agent anyway: %v", err)
	}

	lockNow(t, w)
	if err := w.s.AssignAgent(w.ana, w.anaAgent.ID, 0, Opts{}); !IsForbidden(err) {
		t.Errorf("a player moved a locked-in Agent out of its campaign: %v", err)
	}
	if err := w.s.AssignAgent(w.seer, w.anaAgent.ID, 0, Opts{}); err != nil {
		t.Errorf("the Seer should still be able to move it: %v", err)
	}
}

// TestContactLockedFields: a starting contact's identity locks with its Agent; affection and
// distance don't, and deleting (losing) a contact is never blocked by the lock.
func TestContactLockedFields(t *testing.T) {
	w := setup(t)
	id := w.anaAgent.ID
	ct := &db.Contact{AgentID: id, CampaignID: w.camp.ID, Name: "Mother Agnese", Kind: "Homeland", Affection: 2}
	must(t, w.s.Create(w.ana, "contact", ct, Opts{}))
	lockNow(t, w)

	if _, err := w.s.Update(w.ana, "contact", ct.ID, patch(t, map[string]any{"name": "Someone Else"}), Opts{}); !IsForbidden(err) {
		t.Errorf("player renamed a starting contact once locked: %v", err)
	}
	if _, err := w.s.Update(w.ana, "contact", ct.ID, patch(t, map[string]any{"affection": 4}), Opts{}); err != nil {
		t.Errorf("affection should stay editable once locked: %v", err)
	}
	if err := w.s.Delete(w.ana, "contact", ct.ID, Opts{}); err != nil {
		t.Errorf("losing a contact should stay allowed once locked: %v", err)
	}

	// A contact made after the lock isn't covered by it.
	later := &db.Contact{AgentID: id, CampaignID: w.camp.ID, Name: "New Friend", Kind: "Fellow Agent"}
	must(t, w.s.Create(w.ana, "contact", later, Opts{}))
	if _, err := w.s.Update(w.ana, "contact", later.ID, patch(t, map[string]any{"name": "Renamed Friend"}), Opts{}); err != nil {
		t.Errorf("a contact made after the lock shouldn't be locked itself: %v", err)
	}
}

func lockNow(t *testing.T, w *world) bool {
	t.Helper()
	if _, err := w.s.LockAgents(w.seer, w.camp.ID, Opts{}); err != nil {
		t.Fatal(err)
	}
	return true
}

func containsAbility(abs []db.AgentAbility, id string) bool { return abilityIndex(abs, id) >= 0 }

func abilityIndex(abs []db.AgentAbility, id string) int {
	for i, a := range abs {
		if a.ID == id {
			return i
		}
	}
	return -1
}

// TestGrantAbilityContact covers naming a contact-granting ability's contact after the fact (the
// wizard and sheet both prompt for this when it's still missing), that it's a no-op once named,
// that removing the ability removes that contact too, and the lock.
func TestGrantAbilityContact(t *testing.T) {
	w := setup(t)
	id := w.anaAgent.ID // hunter; the-old-ways belongs to prowler
	if !AbilityGrantsContact("the-old-ways") || AbilityGrantsContact("butcher") {
		t.Error("AbilityGrantsContact")
	}
	teacher := &db.Contact{AgentID: id, CampaignID: w.camp.ID, Name: "Old Marco", Kind: "Fellow Agent", Affection: 6}
	must(t, w.s.Create(w.ana, "contact", teacher, Opts{}))

	ag, err := w.s.AddAbility(w.ana, id, "the-old-ways", "", "", nil, Opts{})
	must(t, err)
	if ag.Abilities[0].GrantedContactID != nil {
		t.Fatal("shouldn't have a contact yet: it wasn't named")
	}

	ag, err = w.s.GrantAbilityContact(w.ana, id, 0, "Old Verminus", "a rat god", Opts{})
	must(t, err)
	if ag.Abilities[0].GrantedContactID == nil {
		t.Fatal("expected a granted contact")
	}
	var ct db.Contact
	must(t, w.s.DB.First(&ct, *ag.Abilities[0].GrantedContactID).Error)
	if ct.Name != "Old Verminus" || ct.Kind != "Deity (The Old Ways)" {
		t.Errorf("contact = %+v", ct)
	}

	// Calling again is a no-op: it already has one.
	ag2, err := w.s.GrantAbilityContact(w.ana, id, 0, "Someone Else", "", Opts{})
	must(t, err)
	if *ag2.Abilities[0].GrantedContactID != ct.ID {
		t.Error("should be a no-op once already granted")
	}

	// Removing the ability removes the contact too, pre-lock.
	ag3, err := w.s.RemoveAbility(w.ana, id, 0, Opts{})
	must(t, err)
	if len(ag3.Abilities) != 0 {
		t.Errorf("abilities after removal = %+v", ag3.Abilities)
	}
	if err := w.s.DB.First(&db.Contact{}, ct.ID).Error; err == nil {
		t.Error("the granted contact should have been deleted")
	}

	// Once locked, only the Seer may finish naming it.
	ag4, err := w.s.AddAbility(w.ana, id, "the-old-ways", "", "", nil, Opts{})
	must(t, err)
	_ = ag4
	lockNow(t, w)
	if _, err := w.s.GrantAbilityContact(w.ana, id, 0, "Nope", "", Opts{}); !IsForbidden(err) {
		t.Errorf("player named a locked ability's contact: %v", err)
	}
	if _, err := w.s.GrantAbilityContact(w.seer, id, 0, "Seer Named", "", Opts{}); err != nil {
		t.Errorf("Seer should still be able to: %v", err)
	}
}
