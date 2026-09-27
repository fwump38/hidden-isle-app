package web

import (
	"net/url"
	"strings"
	"testing"

	"github.com/fwump38/hidden-isle-app/internal/campaign"
	"github.com/fwump38/hidden-isle-app/internal/db"
)

// TestCreateAgentFlowAutomatic exercises the richer creation flow end to end: class select
// creates the Agent, the path chooser offers Automatic (and Manual, and Guided when chat is on),
// and Automatic fills in the whole sheet plus both contacts through the normal validated paths.
func TestCreateAgentFlowAutomatic(t *testing.T) {
	st, svc := newSite(t)

	code, body := st.get("Ana", "/agents/create")
	if code != 200 || !strings.Contains(body, "Occultist") || !strings.Contains(body, "Prowler") {
		t.Fatalf("class select page: %d %q", code, body)
	}
	if strings.Contains(body, "Help me choose") {
		t.Error("chat is off in this test; the class page shouldn't offer it")
	}

	code, flash := st.post("Ana", "/agents/create", url.Values{"name": {"Vex"}, "class": {"occultist"}, "campaign_id": {"0"}})
	if code != 303 || flash != "" {
		t.Fatalf("create: %d %q", code, flash)
	}
	code, body = st.get("Ana", "/agents/1/creation-path")
	if code != 200 || !strings.Contains(body, "Automatic") || !strings.Contains(body, "Manual") {
		t.Fatalf("creation-path page: %d %q", code, body)
	}
	if strings.Contains(body, "Guided") {
		t.Error("chat is off in this test; the path chooser shouldn't offer Guided")
	}

	// Bram can't run creation for Ana's Agent.
	code, flash = st.post("Bram", "/agents/1/creation/automatic", nil)
	if code != 303 || flash == "" {
		t.Fatalf("expected Bram to be refused, got %d %q", code, flash)
	}

	code, flash = st.post("Ana", "/agents/1/creation/automatic", nil)
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
	code, body = st.get("Ana", "/agents/1/wizard?step=12")
	if code != 200 || strings.Contains(body, "Abilities: ,") {
		t.Fatalf("step 12 didn't resolve ability names: %d %q", code, body)
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

func TestCreateAgentForOtherPlayerBySeer(t *testing.T) {
	st, svc := newSite(t)
	code, flash := st.post("Seer", "/campaigns", url.Values{"name": {"Test"}, "mode": {"group"}})
	if code != 303 || flash != "" {
		t.Fatalf("create campaign: %d %q", code, flash)
	}
	code, flash = st.post("Seer", "/agents/create", url.Values{"name": {"Cyrus"}, "class": {"prowler"}, "campaign_id": {"1"}, "owner_id": {"2"}})
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
