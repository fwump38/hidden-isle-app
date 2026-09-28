package web

import (
	"net/url"
	"strings"
	"testing"

	"github.com/fwump38/hidden-isle-app/internal/campaign"
	"github.com/fwump38/hidden-isle-app/internal/db"
)

// TestCreateAgentFlowAutomatic exercises the richer creation flow end to end: the path chooser
// offers Step by step and Automatic before anything else, choosing a class creates the Agent, and
// Automatic fills in the whole sheet plus both contacts through the normal validated paths.
func TestCreateAgentFlowAutomatic(t *testing.T) {
	st, svc := newSite(t)

	code, body := st.get("Ana", "/agents/create")
	if code != 200 || !strings.Contains(body, "Automatic") || !strings.Contains(body, "Step by step") {
		t.Fatalf("path chooser: %d %q", code, body)
	}
	if strings.Contains(body, "Guided") || strings.Contains(body, "chat") {
		t.Error("creation no longer goes through the chat; the path chooser shouldn't mention it")
	}

	code, body = st.get("Ana", "/agents/create/class?path=automatic")
	if code != 200 || !strings.Contains(body, "Occultist") || !strings.Contains(body, "Prowler") {
		t.Fatalf("class select page: %d %q", code, body)
	}
	if !strings.Contains(body, "Surprise me") {
		t.Error("the automatic class page should offer a random draw")
	}
	if strings.Contains(body, "Suggest a class") {
		t.Error("chat is off in this test; the class page shouldn't offer suggestions")
	}
	if strings.Contains(body, `name="name"`) {
		t.Error("automatic draws its own name; the class page shouldn't ask for one")
	}

	code, flash := st.post("Ana", "/agents/create/class", url.Values{"path": {"automatic"}, "class": {"occultist"}, "campaign_id": {"0"}})
	if code != 303 || flash != "" {
		t.Fatalf("automatic: %d %q", code, flash)
	}

	var ag db.Agent
	if err := svc.DB.First(&ag, 1).Error; err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{ag.ChildCard, ag.ChildPhrase, ag.AdultCard, ag.AdultPhrase, ag.AdultVerb,
		ag.BurdenCard, ag.Burden, ag.IdealCard, ag.Ideal, ag.Name, ag.Culture, ag.Look, ag.Why} {
		if f == "" {
			t.Errorf("automatic creation left a field empty on %+v", ag)
		}
	}
	if len(ag.Abilities) == 0 {
		t.Error("automatic creation chose no abilities")
	}

	// The sheet stores only ability ids; the wizard's review step must resolve them to their
	// names from the class data, not show them blank (a real bug caught by this smoke test).
	code, body = st.get("Ana", "/agents/1/wizard?step=done")
	if code != 200 || strings.Contains(body, "Abilities: ,") {
		t.Fatalf("the review step didn't resolve ability names: %d %q", code, body)
	}
	for _, ab := range ag.Abilities {
		found := false
		for _, x := range svc.Data.Current().Class(ag.Class).Abilities {
			if x.ID == ab.ID && strings.Contains(body, x.Name) {
				found = true
			}
		}
		if !found {
			t.Errorf("ability %q's name not shown on the review step", ab.ID)
		}
	}
	total := 0
	for _, v := range ag.Skills {
		total += v
	}
	if total == 0 {
		t.Error("automatic creation allocated no skill points")
	}

	var contacts []db.Contact
	svc.DB.Where("agent_id = ?", ag.ID).Find(&contacts)
	if len(contacts) != 2 {
		t.Fatalf("contacts = %d, want 2", len(contacts))
	}
	kinds := map[string]bool{}
	for _, c := range contacts {
		kinds[c.Kind] = true
		if c.Name == "" || c.CampaignID != ag.CampaignID {
			t.Errorf("contact %+v not wired up correctly", c)
		}
	}
	if !kinds["Homeland"] || !kinds["Dioscorian"] {
		t.Errorf("contacts = %+v, want Homeland and Dioscorian", contacts)
	}

	// The event log records why: automatic creation, not silent.
	var ana db.User
	if err := svc.DB.Where("name = ?", "Ana").First(&ana).Error; err != nil {
		t.Fatal(err)
	}
	evs, err := svc.AgentEvents(campaign.Actor{User: &ana, Via: "web"}, ag.ID, 50)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range evs {
		if strings.Contains(e.Reason, "automatic creation") {
			found = true
		}
	}
	if !found {
		t.Error("expected an event log entry crediting automatic creation")
	}
}

// TestCreateAgentRandomClass covers the Automatic path's "Surprise me" draw: no class value is
// submitted, so the server itself must pick one and run the same generator as a normal pick.
func TestCreateAgentRandomClass(t *testing.T) {
	st, svc := newSite(t)
	code, flash := st.post("Ana", "/agents/create/class", url.Values{"path": {"automatic"}, "random": {"1"}, "campaign_id": {"0"}})
	if code != 303 || flash != "" {
		t.Fatalf("random class: %d %q", code, flash)
	}
	var ag db.Agent
	if err := svc.DB.First(&ag, 1).Error; err != nil {
		t.Fatal(err)
	}
	if ag.Class == "" {
		t.Error("random draw left the class empty")
	}
}

// TestCreateAgentStepByStep covers the step-by-step path: its class page has neither the random
// draw nor a name field (naming is the wizard's first screen, after the class), and choosing a
// class creates the Agent, pre-filled skills and all, and sends the player straight to the wizard.
// The old Guided and Manual links land on the same path.
func TestCreateAgentStepByStep(t *testing.T) {
	st, svc := newSite(t)
	for _, path := range []string{"step", "manual", "guided"} {
		code, body := st.get("Ana", "/agents/create/class?path="+path)
		if code != 200 || !strings.Contains(body, "Occultist") {
			t.Fatalf("class select page (%s): %d %q", path, code, body)
		}
		if strings.Contains(body, "Surprise me") {
			t.Errorf("the %s class page shouldn't offer a random draw", path)
		}
		if strings.Contains(body, `name="name"`) {
			t.Errorf("the %s class page shouldn't ask for a name; the wizard does, after the class", path)
		}
	}

	code, loc := st.postLocation("Ana", "/agents/create/class", url.Values{"path": {"step"}, "class": {"occultist"}, "campaign_id": {"0"}})
	if code != 303 || loc != "/agents/1/wizard" {
		t.Fatalf("step by step should go straight to the wizard, not the chat: %d %q", code, loc)
	}
	var ag db.Agent
	if err := svc.DB.First(&ag, 1).Error; err != nil {
		t.Fatal(err)
	}
	if ag.Class != "occultist" || ag.Name != "New Agent" {
		t.Errorf("agent = %q %q, want an unnamed occultist", ag.Name, ag.Class)
	}
	if ag.Skills["Channel"] != 2 || ag.Skills["Unleash"] != 1 {
		t.Errorf("creation should pre-fill the class's skills, got %v", ag.Skills)
	}
	code, body := st.get("Ana", "/agents/1/wizard")
	if code != 200 || !strings.Contains(body, "called?") || !strings.Contains(body, "Change class") {
		t.Fatalf("wizard should open on the Name step, with a way back to the class: %d %q", code, body)
	}
}

// TestCreateAgentChangeClassFromWizard covers Back from the wizard's first screen: the class page
// for an existing Agent, where picking another class swaps in its pre-filled skills and clears
// abilities chosen for the old one.
func TestCreateAgentChangeClassFromWizard(t *testing.T) {
	st, svc := newSite(t)
	if code, _ := st.post("Ana", "/agents/create/class", url.Values{"path": {"step"}, "class": {"occultist"}, "campaign_id": {"0"}}); code != 303 {
		t.Fatal("create")
	}
	if code, flash := st.post("Ana", "/agents/1", url.Values{"ability.add": {"evil-eye"}}); code != 303 || flash != "" {
		t.Fatalf("add ability: %d %q", code, flash)
	}
	code, body := st.get("Ana", "/agents/create/class?agent=1&campaign_id=0")
	if code != 200 || !strings.Contains(body, `name="agent_id" value="1"`) || !strings.Contains(body, "Switch to Prowler") {
		t.Fatalf("change-class page: %d %q", code, body)
	}
	if code, _ := st.get("Bram", "/agents/create/class?agent=1"); code == 200 {
		t.Error("another player shouldn't be able to change Ana's Agent's class")
	}
	code, loc := st.postLocation("Ana", "/agents/create/class", url.Values{"path": {"step"}, "class": {"prowler"}, "agent_id": {"1"}})
	if code != 303 || loc != "/agents/1/wizard?step=name" {
		t.Fatalf("change class: %d %q", code, loc)
	}
	var ag db.Agent
	if err := svc.DB.First(&ag, 1).Error; err != nil {
		t.Fatal(err)
	}
	if ag.Class != "prowler" || ag.Skills["Slip"] != 2 || ag.Skills["Channel"] != 0 || len(ag.Abilities) != 0 {
		t.Errorf("after the change: class %q skills %v abilities %v", ag.Class, ag.Skills, ag.Abilities)
	}
	var n int64
	svc.DB.Model(&db.Agent{}).Count(&n)
	if n != 1 {
		t.Errorf("changing class created another Agent (%d total)", n)
	}
}

func TestCreateAgentForOtherPlayerBySeer(t *testing.T) {
	st, svc := newSite(t)
	code, flash := st.post("Seer", "/campaigns", url.Values{"name": {"Test"}, "mode": {"group"}})
	if code != 303 || flash != "" {
		t.Fatalf("create campaign: %d %q", code, flash)
	}
	code, flash = st.post("Seer", "/agents/create/class", url.Values{"path": {"guided"}, "name": {"Cyrus"}, "class": {"prowler"}, "campaign_id": {"1"}, "owner_id": {"2"}})
	if code != 303 || flash != "" {
		t.Fatalf("create for player: %d %q", code, flash)
	}
	var ag db.Agent
	if err := svc.DB.First(&ag, 1).Error; err != nil {
		t.Fatal(err)
	}
	if ag.OwnerID == nil || *ag.OwnerID != 2 || ag.CampaignID != 1 {
		t.Errorf("agent = %+v, want owner 2 in campaign 1", ag)
	}
	var member db.Member
	if err := svc.DB.Where("campaign_id = ? AND user_id = ?", 1, 2).First(&member).Error; err != nil {
		t.Error("the Seer's pick should have added the player to the campaign")
	}
}
