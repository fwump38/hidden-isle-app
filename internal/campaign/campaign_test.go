package campaign

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/fwump38/hidden-isle-app/internal/db"
	"github.com/fwump38/hidden-isle-app/internal/gamedata"
)

func fixtureSnap(t *testing.T) *gamedata.Snapshot {
	t.Helper()
	s := &gamedata.Snapshot{}
	b, err := os.ReadFile("testdata/limits.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if err := yaml.Unmarshal(b, &s.Limits); err != nil {
		t.Fatal(err)
	}
	for suit, sks := range map[string][]string{"Swords": {"Skirmish", "Convince", "Study"}, "Wands": {"Unleash", "Perform", "Channel"},
		"Cups": {"Slip", "Soothe", "Mingle"}, "Pentacles": {"Finesse", "Bargain", "Survey"}} {
		for _, sk := range sks {
			s.Skills.Skills = append(s.Skills.Skills, gamedata.Skill{Name: sk, Suit: suit})
		}
	}
	s.Classes.Classes = []gamedata.Class{{ID: "hunter", Name: "Hunter", PrefilledSkills: map[string]int{"Skirmish": 1, "Unleash": 2},
		Abilities: []gamedata.Ability{{ID: "butcher", Name: "BUTCHER", Text: "…", Page: 49}}}}
	s.Campaign = gamedata.Campaign{
		AgentStatus: []string{"Active", "Dead"}, ContactKind: []string{"Homeland", "Dioscorian", "Deity (The Old Ways)", "Fellow Agent"}, SessionStatus: []string{"Prep", "Played"},
		AdversaryStatus: []string{"Rumored", "Active"}, ClockScope: []string{"Scenario", "Ability"}, ClockStatus: []string{"Running", "Filled"},
		Territories: []string{"Dioscoria", "Venice"}, CampaignMode: []string{"group", "solitaire"},
	}
	return s
}

type world struct {
	s         *Service
	seer, ana Actor
	bram, eve Actor // bram: another player in the campaign; eve: not a member
	camp      *db.Campaign
	anaAgent  *db.Agent
	bramAgent *db.Agent
}

func setup(t *testing.T) *world {
	t.Helper()
	g, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	w := &world{s: New(g, gamedata.StaticStore(fixtureSnap(t)))}
	mk := func(name string, role db.Role) Actor {
		u := &db.User{Name: name, Role: role, Active: true}
		if err := g.Create(u).Error; err != nil {
			t.Fatal(err)
		}
		return Actor{User: u, Via: "web"}
	}
	w.seer, w.ana, w.bram, w.eve = mk("Seer", db.RoleSeer), mk("Ana", db.RolePlayer), mk("Bram", db.RolePlayer), mk("Eve", db.RolePlayer)
	w.camp = &db.Campaign{Name: "Test"}
	must(t, w.s.CreateCampaign(w.seer, w.camp, Opts{}))
	must(t, w.s.SetMember(w.seer, w.camp.ID, w.ana.User.ID, true))
	must(t, w.s.SetMember(w.seer, w.camp.ID, w.bram.User.ID, true))
	w.anaAgent, err = w.s.NewAgent(w.ana, w.camp.ID, "Ines", "hunter", nil, Opts{})
	must(t, err)
	w.bramAgent, err = w.s.NewAgent(w.bram, w.camp.ID, "Cyrus", "hunter", nil, Opts{})
	must(t, err)
	return w
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func patch(t *testing.T, kv map[string]any) Patch {
	p := Patch{}
	for k, v := range kv {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		p[k] = b
	}
	return p
}

func TestNewAgentPrefillsAndOwnership(t *testing.T) {
	w := setup(t)
	if w.anaAgent.Skills["Unleash"] != 2 || w.anaAgent.Skills["Skirmish"] != 1 {
		t.Errorf("pre-filled skills missing: %v", w.anaAgent.Skills)
	}
	if w.anaAgent.OwnerID == nil || *w.anaAgent.OwnerID != w.ana.User.ID {
		t.Error("a player's new Agent must belong to them")
	}
	other := w.bram.User.ID
	ag, err := w.s.NewAgent(w.ana, w.camp.ID, "Sneaky", "hunter", &other, Opts{})
	must(t, err)
	if *ag.OwnerID != w.ana.User.ID {
		t.Error("a player can't create an Agent owned by someone else")
	}
	if _, err := w.s.NewAgent(w.eve, w.camp.ID, "Outsider", "hunter", nil, Opts{}); !IsForbidden(err) {
		t.Errorf("non-member created an Agent: %v", err)
	}
}

func TestPlayersEditOnlyTheirOwnAgent(t *testing.T) {
	w := setup(t)
	if _, err := w.s.Update(w.ana, "agent", w.anaAgent.ID, patch(t, map[string]any{"burden_track": 2}), Opts{Reason: "reckless"}); err != nil {
		t.Fatalf("owner edit: %v", err)
	}
	if _, err := w.s.Update(w.ana, "agent", w.bramAgent.ID, patch(t, map[string]any{"burden_track": 2}), Opts{}); !IsForbidden(err) {
		t.Errorf("edited another player's Agent: %v", err)
	}
	if _, err := w.s.Update(w.ana, "agent", w.anaAgent.ID, patch(t, map[string]any{"owner_id": w.bram.User.ID}), Opts{}); !IsForbidden(err) {
		t.Errorf("player changed owner: %v", err)
	}
	if _, err := w.s.Update(w.ana, "agent", w.anaAgent.ID, patch(t, map[string]any{"status": "Dead"}), Opts{}); err != nil {
		t.Errorf("owner should be able to set status: %v", err)
	}
	if _, err := w.s.Update(w.bram, "agent", w.anaAgent.ID, patch(t, map[string]any{"status": "Active"}), Opts{}); !IsForbidden(err) {
		t.Errorf("another player changed status: %v", err)
	}
	if _, err := w.s.Update(w.eve, "agent", w.anaAgent.ID, patch(t, map[string]any{"notes": "x"}), Opts{}); !IsForbidden(err) {
		t.Errorf("non-member edited: %v", err)
	}
	if _, err := w.s.Update(w.seer, "agent", w.bramAgent.ID, patch(t, map[string]any{"status": "Dead"}), Opts{}); err != nil {
		t.Errorf("Seer edit: %v", err)
	}
	if err := w.s.Delete(w.bram, "agent", w.anaAgent.ID, Opts{}); !IsForbidden(err) {
		t.Errorf("player deleted another player's Agent: %v", err)
	}
	if err := w.s.Delete(w.ana, "agent", w.anaAgent.ID, Opts{}); err != nil {
		t.Errorf("owner delete: %v", err)
	}
}

func TestAgentLimits(t *testing.T) {
	w := setup(t)
	bad := []map[string]any{
		{"burden_track": 8},
		{"skills": map[string]int{"Slip": 4}},
		{"skills": map[string]int{"Juggle": 1}},
		{"harm": map[string][]string{"Cups": {"P", "S", "P"}}},
		{"harm": map[string][]string{"Cups": {"X"}}},
		{"virtues": []string{"a", "b", "c", "d"}},
		{"load_used": 6},
		{"xp_cups": 8},
		{"abilities": []db.AgentAbility{{ID: "no-such-ability"}}},
	}
	if _, err := w.s.Update(w.seer, "agent", w.anaAgent.ID, patch(t, map[string]any{"class": "juggler"}), Opts{}); err == nil {
		t.Error("unknown class accepted")
	}
	for _, p := range bad {
		_, err := w.s.Update(w.ana, "agent", w.anaAgent.ID, patch(t, p), Opts{})
		var ve *ValidationError
		if !errors.As(err, &ve) {
			t.Errorf("%v: expected a validation error, got %v", p, err)
		}
	}
	// The 4th pip, once unlocked, allows 4.
	if _, err := w.s.Update(w.ana, "agent", w.anaAgent.ID, patch(t, map[string]any{
		"unlocked_fourth": []string{"Slip"}, "skills": map[string]int{"Slip": 4}}), Opts{}); err != nil {
		t.Errorf("unlocked 4th pip rejected: %v", err)
	}
	// A player can't override; the Seer can, with a reason, which records a house ruling.
	if _, err := w.s.Update(w.ana, "agent", w.anaAgent.ID, patch(t, map[string]any{"load_used": 6}), Opts{Override: true, Reason: "x"}); err == nil {
		t.Error("player override accepted")
	}
	if _, err := w.s.Update(w.seer, "agent", w.anaAgent.ID, patch(t, map[string]any{"load_used": 6}), Opts{Override: true}); err == nil {
		t.Error("Seer override without a reason accepted")
	}
	if _, err := w.s.Update(w.seer, "agent", w.anaAgent.ID, patch(t, map[string]any{"load_used": 6}), Opts{Override: true, Reason: "overburdened with loot"}); err != nil {
		t.Errorf("Seer override: %v", err)
	}
	var rulings []db.HouseRuling
	must(t, w.s.List(w.seer, "house_ruling", w.camp.ID, &rulings, ""))
	if len(rulings) != 1 || !strings.Contains(rulings[0].Ruling, "overburdened") {
		t.Errorf("override should record a house ruling: %+v", rulings)
	}
}

func TestEventsAndUndo(t *testing.T) {
	w := setup(t)
	_, err := w.s.Update(w.ana, "agent", w.anaAgent.ID, patch(t, map[string]any{"harm": map[string][]string{"Cups": {"P", "S"}}}), Opts{Reason: "failed Slip"})
	must(t, err)
	evs, err := w.s.Events(w.ana, w.camp.ID, EventFilter{EntityType: "agent", EntityID: w.anaAgent.ID})
	must(t, err)
	if len(evs) == 0 || evs[0].Reason != "failed Slip" || len(evs[0].Changes) != 1 || evs[0].Changes[0].Field != "harm" {
		t.Fatalf("unexpected events: %+v", evs)
	}
	harmEv := evs[0]

	// A later change to the same field blocks undoing the earlier one.
	_, err = w.s.Update(w.seer, "agent", w.anaAgent.ID, patch(t, map[string]any{"harm": map[string][]string{"Cups": {"P"}}}), Opts{Reason: "healed"})
	must(t, err)
	var ce *ConflictError
	if err := w.s.Revert(w.seer, harmEv.ID, ""); !errors.As(err, &ce) {
		t.Fatalf("expected a conflict, got %v", err)
	}
	evs, _ = w.s.Events(w.seer, w.camp.ID, EventFilter{EntityType: "agent", EntityID: w.anaAgent.ID, Limit: 1})
	// Bram can't undo the Seer's change; the Seer can.
	if err := w.s.Revert(w.bram, evs[0].ID, ""); !IsForbidden(err) {
		t.Errorf("another player undid a change: %v", err)
	}
	must(t, w.s.Revert(w.seer, evs[0].ID, ""))
	must(t, w.s.Revert(w.ana, harmEv.ID, "")) // now nothing blocks it, and it's Ana's own change
	ag, _ := w.s.Agent(w.ana, w.anaAgent.ID)
	if len(ag.Harm["Cups"]) != 0 {
		t.Errorf("harm should be back to none: %v", ag.Harm)
	}
	if err := w.s.Revert(w.ana, harmEv.ID, ""); err == nil {
		t.Error("undid the same change twice")
	}

	// Delete and restore a contact.
	c := &db.Contact{CampaignID: w.camp.ID, AgentID: w.anaAgent.ID, Name: "Mother Agnese", Kind: "Homeland", Affection: 4}
	must(t, w.s.Create(w.ana, "contact", c, Opts{}))
	if err := w.s.Create(w.bram, "contact", &db.Contact{CampaignID: w.camp.ID, AgentID: w.anaAgent.ID, Name: "x"}, Opts{}); !IsForbidden(err) {
		t.Errorf("added a contact to another player's Agent: %v", err)
	}
	must(t, w.s.Delete(w.ana, "contact", c.ID, Opts{}))
	evs, _ = w.s.Events(w.ana, w.camp.ID, EventFilter{EntityType: "contact", Limit: 1})
	must(t, w.s.Revert(w.ana, evs[0].ID, ""))
	cs, _ := w.s.Contacts(w.ana, w.anaAgent.ID)
	if len(cs) != 1 || cs[0].Affection != 4 {
		t.Errorf("contact not restored: %+v", cs)
	}
}

func TestSeerSecretsStaySecret(t *testing.T) {
	w := setup(t)
	adv := &db.Adversary{CampaignID: w.camp.ID, Name: "Death cult", Status: "Rumored", TrackLength: 8, Secrets: "the Doge is the leader"}
	must(t, w.s.Create(w.seer, "adversary", adv, Opts{}))
	hidden := &db.Adversary{CampaignID: w.camp.ID, Name: "The Hollow Crown", Status: "Rumored", Hidden: true}
	must(t, w.s.Create(w.seer, "adversary", hidden, Opts{}))
	secretClock := &db.Clock{CampaignID: w.camp.ID, Name: "The ritual completes", Segments: 6, Status: "Running", Visibility: db.VisSeer}
	must(t, w.s.Create(w.seer, "clock", secretClock, Opts{}))
	sess := &db.Session{CampaignID: w.camp.ID, Number: 1, Title: "Arrival", Status: "Prep", Prep: "twist: the contact is the spy"}
	must(t, w.s.Create(w.seer, "session", sess, Opts{}))
	_, err := w.s.Update(w.seer, "session", sess.ID, patch(t, map[string]any{"prep": "twist changed", "summary": "Find the lost map"}), Opts{})
	must(t, err)
	must(t, w.s.Create(w.seer, "seer_note", &db.SeerNote{CampaignID: w.camp.ID, Title: "What the Hand hasn't learned", Body: "…"}, Opts{}))

	var advs []db.Adversary
	must(t, w.s.List(w.ana, "adversary", w.camp.ID, &advs, ""))
	if len(advs) != 1 || advs[0].Secrets != "" {
		t.Errorf("player sees hidden adversaries or secrets: %+v", advs)
	}
	var clocks []db.Clock
	must(t, w.s.List(w.ana, "clock", w.camp.ID, &clocks, ""))
	if len(clocks) != 0 {
		t.Errorf("player sees a Seer-only clock: %+v", clocks)
	}
	var sessions []db.Session
	must(t, w.s.List(w.ana, "session", w.camp.ID, &sessions, ""))
	if len(sessions) != 1 || sessions[0].Prep != "" || sessions[0].Summary != "Find the lost map" {
		t.Errorf("session prep leaked or summary missing: %+v", sessions)
	}
	if got, err := w.s.Get(w.ana, "session", sess.ID); err != nil || got.(*db.Session).Prep != "" {
		t.Errorf("Get leaked prep: %v %v", got, err)
	}
	if _, err := w.s.Get(w.ana, "adversary", hidden.ID); !IsNotFound(err) {
		t.Errorf("Get showed a hidden adversary: %v", err)
	}
	var notes []db.SeerNote
	if err := w.s.List(w.ana, "seer_note", w.camp.ID, &notes, ""); !IsForbidden(err) {
		t.Errorf("player listed Seer notes: %v", err)
	}
	if _, err := w.s.Update(w.ana, "session", sess.ID, patch(t, map[string]any{"title": "x"}), Opts{}); !IsForbidden(err) {
		t.Errorf("player edited a session: %v", err)
	}

	evs, err := w.s.Events(w.ana, w.camp.ID, EventFilter{Limit: 200})
	must(t, err)
	for _, e := range evs {
		b, _ := json.Marshal(e)
		for _, secret := range []string{"Doge", "Hollow Crown", "ritual", "twist", "Hand hasn't learned"} {
			if strings.Contains(string(b), secret) || strings.Contains(string(e.Snapshot), secret) {
				t.Errorf("player's change log leaks %q: %s", secret, e.Summary)
			}
		}
	}
	seerEvs, _ := w.s.Events(w.seer, w.camp.ID, EventFilter{Limit: 200})
	if len(seerEvs) <= len(evs) {
		t.Error("the Seer should see more of the log than a player")
	}
	if _, err := w.s.Events(w.eve, w.camp.ID, EventFilter{}); !IsForbidden(err) {
		t.Errorf("non-member read the log: %v", err)
	}
}

func TestPlayersTickOnlyTheirOwnAbilityClocks(t *testing.T) {
	w := setup(t)
	mine := &db.Clock{CampaignID: w.camp.ID, Name: "Law of reflection", Segments: 3, Scope: "Ability", Status: "Running", Visibility: db.VisParty, AgentID: &w.anaAgent.ID}
	must(t, w.s.Create(w.seer, "clock", mine, Opts{}))
	if _, err := w.s.Update(w.ana, "clock", mine.ID, patch(t, map[string]any{"filled": 1}), Opts{}); err != nil {
		t.Errorf("owner tick: %v", err)
	}
	if _, err := w.s.Update(w.bram, "clock", mine.ID, patch(t, map[string]any{"filled": 2}), Opts{}); !IsForbidden(err) {
		t.Errorf("another player ticked it: %v", err)
	}
	if _, err := w.s.Update(w.ana, "clock", mine.ID, patch(t, map[string]any{"segments": 8}), Opts{}); !IsForbidden(err) {
		t.Errorf("player resized a clock: %v", err)
	}
	if _, err := w.s.Update(w.seer, "clock", mine.ID, patch(t, map[string]any{"segments": 5}), Opts{}); err == nil {
		t.Error("5-segment clock accepted")
	}
}

func TestEntriesVisibility(t *testing.T) {
	w := setup(t)
	private := &db.Entry{CampaignID: w.camp.ID, Kind: "journal", Title: "Dear diary", Body: "I don't trust Cyrus", Visibility: db.VisPrivate}
	must(t, w.s.WriteEntry(w.ana, private))
	shared := &db.Entry{CampaignID: w.camp.ID, Kind: "journal", Title: "For the Seer", Visibility: db.VisOwner}
	must(t, w.s.WriteEntry(w.ana, shared))
	recap := &db.Entry{CampaignID: w.camp.ID, Kind: "recap", Title: "Session 1", Visibility: db.VisParty}
	must(t, w.s.WriteEntry(w.seer, recap))
	if err := w.s.WriteEntry(w.ana, &db.Entry{CampaignID: w.camp.ID, Kind: "recap", Title: "fake"}); !IsForbidden(err) {
		t.Errorf("player wrote a recap: %v", err)
	}
	if err := w.s.WriteEntry(w.ana, &db.Entry{CampaignID: w.camp.ID, Kind: "history", AgentID: &w.bramAgent.ID, Title: "x"}); !IsForbidden(err) {
		t.Errorf("player wrote history for another's Agent: %v", err)
	}

	titles := func(a Actor) map[string]bool {
		es, err := w.s.Entries(a, w.camp.ID, "", 0)
		must(t, err)
		m := map[string]bool{}
		for _, e := range es {
			m[e.Title] = true
		}
		return m
	}
	if got := titles(w.bram); got["Dear diary"] || got["For the Seer"] || got["Session 1"] {
		t.Errorf("Bram sees others' journals or an unpublished recap: %v", got)
	}
	if got := titles(w.seer); got["Dear diary"] || !got["For the Seer"] {
		t.Errorf("Seer should see shared journals but not private ones: %v", got)
	}
	recap.Published = true
	must(t, w.s.WriteEntry(w.seer, recap))
	if !titles(w.bram)["Session 1"] {
		t.Error("published recap not visible to players")
	}
	private.Body = "edited by Bram"
	if err := w.s.WriteEntry(w.bram, private); !IsForbidden(err) {
		t.Errorf("Bram edited Ana's journal: %v", err)
	}
}

func TestDescribe(t *testing.T) {
	ch := func(field, from, to string) db.Change {
		return db.Change{Field: field, From: json.RawMessage(from), To: json.RawMessage(to)}
	}
	for _, tc := range []struct {
		c    db.Change
		want string
	}{
		{ch("skills", `{"Slip":2,"Finesse":1}`, `{"Slip":3,"Finesse":1}`), "Slip 2 → 3"},
		{ch("harm", `{"Cups":["P"]}`, `{"Cups":["P","S"]}`), "harm Cups P → P S"},
		{ch("harm", `null`, `{"Wands":["S"]}`), "harm Wands — → S"},
		{ch("abilities", `[{"id":"wisp"}]`, `[{"id":"wisp"},{"id":"burglar"}]`), "abilities + burglar"},
		{ch("items", `[{"name":"Rope","used":true}]`, `null`), "items − Rope"},
		{ch("vices", `null`, `["Gambling","Drink"]`), "vices — → Gambling Drink"},
		{ch("burden_track", `2`, `3`), "burden track 2 → 3"},
		{ch("burden", `""`, `"Reckless"`), "burden — → Reckless"},
	} {
		if got := describe(tc.c); got != tc.want {
			t.Errorf("describe(%s) = %q, want %q", tc.c.Field, got, tc.want)
		}
	}
}

func TestPrivateAgentsAndCampaigns(t *testing.T) {
	w := setup(t)
	solo, err := w.s.NewAgent(w.eve, 0, "Wanderer", "hunter", nil, Opts{})
	must(t, err)
	if _, err := w.s.Agent(w.ana, solo.ID); !IsNotFound(err) {
		t.Errorf("another player can see a private Agent: %v", err)
	}
	if _, err := w.s.Agent(w.seer, solo.ID); err != nil {
		t.Errorf("Seer can't see a private Agent: %v", err)
	}
	if _, err := w.s.Update(w.eve, "agent", solo.ID, patch(t, map[string]any{"burden_track": 1}), Opts{}); err != nil {
		t.Errorf("owner edit of a private Agent: %v", err)
	}
	must(t, w.s.Create(w.eve, "contact", &db.Contact{AgentID: solo.ID, Name: "Old friend", Kind: "Homeland"}, Opts{}))
	mine, _ := w.s.MyAgents(w.eve)
	if len(mine) != 1 {
		t.Fatalf("MyAgents = %d", len(mine))
	}
	// Eve isn't in the campaign: she can't bring the Agent there.
	if err := w.s.AssignAgent(w.eve, solo.ID, w.camp.ID, Opts{}); !IsForbidden(err) {
		t.Errorf("non-member joined a campaign: %v", err)
	}
	if err := w.s.AssignAgent(w.ana, solo.ID, w.camp.ID, Opts{}); !IsNotFound(err) {
		t.Errorf("another player moved Eve's Agent: %v", err)
	}
	// The Seer moves it in, which makes Eve a member; its contact follows.
	must(t, w.s.AssignAgent(w.seer, solo.ID, w.camp.ID, Opts{}))
	if _, err := w.s.Campaign(w.eve, w.camp.ID); err != nil {
		t.Errorf("owner should now be a member: %v", err)
	}
	if ag, err := w.s.Agent(w.ana, solo.ID); err != nil || ag.CampaignID != w.camp.ID {
		t.Errorf("campaign members should see it now: %v", err)
	}
	cs, _ := w.s.Contacts(w.eve, solo.ID)
	if len(cs) != 1 || cs[0].CampaignID != w.camp.ID {
		t.Errorf("contact didn't follow its Agent: %+v", cs)
	}
	evs, _ := w.s.AgentEvents(w.eve, solo.ID, 50)
	if len(evs) < 3 {
		t.Errorf("Agent log should include changes from before it joined: %d events", len(evs))
	}
	// The owner can take it out again.
	must(t, w.s.AssignAgent(w.eve, solo.ID, 0, Opts{}))
	if _, err := w.s.Agent(w.ana, solo.ID); !IsNotFound(err) {
		t.Errorf("Agent should be private again: %v", err)
	}
	// The Seer creating an Agent for a player adds them to the campaign.
	camp2 := &db.Campaign{Name: "Second"}
	must(t, w.s.CreateCampaign(w.seer, camp2, Opts{}))
	_, err = w.s.NewAgent(w.seer, camp2.ID, "Pregen", "hunter", &w.eve.User.ID, Opts{})
	must(t, err)
	if _, err := w.s.Campaign(w.eve, camp2.ID); err != nil {
		t.Errorf("player not added to the campaign: %v", err)
	}
}

func TestDeleteCampaignAndUser(t *testing.T) {
	w := setup(t)
	npc, err := w.s.NewAgent(w.seer, w.camp.ID, "NPC Agent", "hunter", nil, Opts{})
	must(t, err)
	must(t, w.s.Create(w.seer, "clock", &db.Clock{CampaignID: w.camp.ID, Name: "Doom", Segments: 4, Status: "Running", Visibility: db.VisParty}, Opts{}))
	if err := w.s.DeleteCampaign(w.ana, w.camp.ID, w.camp.Name); !IsForbidden(err) {
		t.Errorf("player deleted a campaign: %v", err)
	}
	if err := w.s.DeleteCampaign(w.seer, w.camp.ID, "wrong name"); err == nil {
		t.Error("deleted without the right confirmation")
	}
	must(t, w.s.DeleteCampaign(w.seer, w.camp.ID, w.camp.Name))
	if ag, err := w.s.Agent(w.ana, w.anaAgent.ID); err != nil || ag.CampaignID != 0 {
		t.Errorf("player's Agent should survive outside any campaign: %v %+v", err, ag)
	}
	if _, err := w.s.Agent(w.seer, npc.ID); !IsNotFound(err) {
		t.Errorf("Seer-run Agent should be deleted with the campaign: %v", err)
	}
	var n int64
	w.s.DB.Model(&db.Clock{}).Count(&n)
	if n != 0 {
		t.Errorf("%d clocks left behind", n)
	}
	w.s.DB.Model(&db.Event{}).Where("campaign_id = ?", w.camp.ID).Count(&n)
	if n != 0 {
		t.Errorf("%d events left behind", n)
	}

	// Deleting a player: campaign Agents go to the Seer, private ones are deleted.
	camp := &db.Campaign{Name: "Again"}
	must(t, w.s.CreateCampaign(w.seer, camp, Opts{}))
	must(t, w.s.AssignAgent(w.seer, w.bramAgent.ID, camp.ID, Opts{}))
	private, _ := w.s.NewAgent(w.bram, 0, "Secret", "hunter", nil, Opts{})
	must(t, w.s.WriteEntry(w.bram, &db.Entry{CampaignID: camp.ID, Kind: "journal", Title: "diary", Visibility: db.VisPrivate}))
	if err := w.s.DeleteUser(w.seer, w.seer.User.ID); err == nil {
		t.Error("deleted the Seer")
	}
	must(t, w.s.DeleteUser(w.seer, w.bram.User.ID))
	if ag, err := w.s.Agent(w.seer, w.bramAgent.ID); err != nil || ag.OwnerID != nil {
		t.Errorf("campaign Agent should now be the Seer's: %v %+v", err, ag)
	}
	if _, err := w.s.Agent(w.seer, private.ID); !IsNotFound(err) {
		t.Errorf("private Agent should be deleted: %v", err)
	}
	w.s.DB.Model(&db.Entry{}).Where("title = ?", "diary").Count(&n)
	if n != 0 {
		t.Error("private journal should be deleted")
	}
}

func TestTableOps(t *testing.T) {
	w := setup(t)
	id := w.anaAgent.ID
	ag, err := w.s.AddHarm(w.seer, id, "cups", "P", 2, Opts{Reason: "fell"})
	must(t, err)
	if strings.Join(ag.Harm["Cups"], "") != "PP" {
		t.Fatalf("harm = %v", ag.Harm)
	}
	if _, err := w.s.AddHarm(w.seer, id, "Cups", "S", 1, Opts{}); err == nil || !strings.Contains(err.Error(), "Swords") {
		t.Errorf("full suit should name suits with room: %v", err)
	}
	ag, err = w.s.AddHarm(w.seer, id, "Cups", "T", 1, Opts{})
	must(t, err)
	if strings.Join(ag.Harm["Cups"], "") != "TP" {
		t.Errorf("trauma should upgrade a mark: %v", ag.Harm["Cups"])
	}
	ag, healed, err := w.s.Heal(w.seer, id, "any", 3, "", Opts{})
	must(t, err)
	if healed != 1 || strings.Join(ag.Harm["Cups"], "") != "T" {
		t.Errorf("heal any should leave trauma: healed %d, %v", healed, ag.Harm)
	}

	ag, note, err := w.s.AwardXP(w.seer, id, "Cups", 9, Opts{})
	must(t, err)
	if ag.XPCups != 7 || !strings.Contains(note, "Track full") || !strings.Contains(note, "2 XP didn't fit") {
		t.Errorf("xp %d, note %q", ag.XPCups, note)
	}

	clock := &db.Clock{CampaignID: w.camp.ID, Name: "Guards arrive", Segments: 4, Status: "Running", Visibility: db.VisParty}
	must(t, w.s.Create(w.seer, "clock", clock, Opts{}))
	c, note, err := w.s.TickClock(w.seer, clock.ID, 9, Opts{})
	must(t, err)
	if c.Filled != 4 || !strings.Contains(note, "full") {
		t.Errorf("tick: %d %q", c.Filled, note)
	}

	adv := &db.Adversary{CampaignID: w.camp.ID, Name: "Cult", Status: "Active", TrackLength: 6}
	must(t, w.s.Create(w.seer, "adversary", adv, Opts{}))
	ad, _, err := w.s.AdvanceAdversary(w.seer, adv.ID, "rapid", Opts{})
	must(t, err)
	if ad.Progress != 2 {
		t.Errorf("progress %d", ad.Progress)
	}
	hidden := &db.Adversary{CampaignID: w.camp.ID, Name: "Hidden", Status: "Rumored", Hidden: true}
	must(t, w.s.Create(w.seer, "adversary", hidden, Opts{}))
	if _, _, err := w.s.AdvanceAdversary(w.seer, hidden.ID, "steady", Opts{}); err == nil {
		t.Error("advanced a hidden adversary")
	}

	must(t, w.s.Create(w.ana, "contact", &db.Contact{CampaignID: w.camp.ID, AgentID: id, Name: "Near", Kind: "Homeland", Affection: 3, Distance: 1}, Opts{}))
	must(t, w.s.Create(w.ana, "contact", &db.Contact{CampaignID: w.camp.ID, AgentID: id, Name: "Far", Kind: "Homeland", Affection: 1, Distance: 2}, Opts{}))
	must(t, w.s.Create(w.ana, "contact", &db.Contact{CampaignID: w.camp.ID, AgentID: id, Name: "Old god", Kind: "Deity (The Old Ways)", Affection: 6}, Opts{}))
	notes, err := w.s.DriftContacts(w.seer, id, Opts{})
	must(t, err)
	cs, _ := w.s.Contacts(w.seer, id)
	got := map[string][2]int{}
	for _, c := range cs {
		got[c.Name] = [2]int{c.Affection, c.Distance}
	}
	if got["Near"] != [2]int{3, 2} || got["Far"] != [2]int{0, 0} || got["Old god"] != [2]int{6, 0} {
		t.Errorf("drift: %v (%v)", got, notes)
	}

	sess, err := w.s.CreateRecord(w.seer, "session", w.camp.ID, patch(t, map[string]any{"title": "Arrival"}), Opts{})
	must(t, err)
	if sess.(*db.Session).Number != 1 {
		t.Errorf("session number %d", sess.(*db.Session).Number)
	}
	if _, err := w.s.CreateRecord(w.seer, "contact", w.camp.ID, patch(t, map[string]any{"name": "No agent"}), Opts{}); err == nil {
		t.Error("contact without agent_id accepted")
	}
	if _, err := w.s.CreateRecord(w.ana, "clock", w.camp.ID, patch(t, map[string]any{"name": "x", "segments": 4}), Opts{}); !IsForbidden(err) {
		t.Errorf("player created a clock: %v", err)
	}
}
