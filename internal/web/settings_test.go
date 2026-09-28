package web

import (
	"net/url"
	"strings"
	"testing"

	"github.com/fwump38/hidden-isle-app/internal/db"
)

func TestCampaignOptions(t *testing.T) {
	c := &db.Campaign{Visions: true, SkipFirstDowntime: true}
	got := campaignOptions(c)
	want := []string{"visions", "no downtime before session one"}
	if len(got) != len(want) {
		t.Fatalf("campaignOptions = %v, want %v", got, want)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("campaignOptions[%d] = %q, want %q", i, got[i], w)
		}
	}
	if got := campaignOptions(&db.Campaign{}); got != nil {
		t.Errorf("no options should give nil, got %v", got)
	}
}

// TestSettingsSavesOptionsAndTable covers the restructured settings page: the three checkboxes
// save as real booleans, and table agreements / open threads save as before.
func TestSettingsSavesOptionsAndTable(t *testing.T) {
	st, svc := newSite(t)
	st.post("Seer", "/campaigns", url.Values{"name": {"Venice"}, "mode": {"group"}})

	if code, flash := st.post("Seer", "/r/campaign/1", url.Values{
		"set.table":   {"3 hours, every other Friday. No graphic torture."},
		"set.visions": {"on"}, "set.thresholds": {"on"}, "set.skip_first_downtime": {"on"},
		"set.options":      {"house rule: fortune cards reshuffle each session"},
		"set.open_threads": {"the mayor's disappearance"},
		"back":             {"/c/1/settings"},
	}); code != 303 || flash != "" {
		t.Fatalf("save settings: %d %q", code, flash)
	}

	var c db.Campaign
	if err := svc.DB.First(&c, 1).Error; err != nil {
		t.Fatal(err)
	}
	if !c.Visions || !c.Thresholds || !c.SkipFirstDowntime {
		t.Errorf("options weren't saved as booleans: %+v", c)
	}
	if c.Table == "" || c.Options == "" || c.OpenThreads == "" {
		t.Errorf("text fields weren't saved: %+v", c)
	}
}

// TestCampaignPageShowsSettingsToPlayers covers the "players can see this" part: table
// agreements, options in use and open threads show on the campaign overview to a player, not
// just the Seer, with an Edit link only for the Seer.
func TestCampaignPageShowsSettingsToPlayers(t *testing.T) {
	st, _ := newSite(t)
	st.post("Seer", "/campaigns", url.Values{"name": {"Venice"}, "mode": {"group"}})
	st.post("Seer", "/c/1/members", url.Values{"user_id": {"2"}, "member": {"on"}})
	st.post("Seer", "/r/campaign/1", url.Values{
		"set.table": {"Every other Friday"}, "set.visions": {"on"}, "set.open_threads": {"a missing ledger"},
	})

	for _, who := range []string{"Seer", "Ana"} {
		_, body := st.get(who, "/c/1")
		for _, want := range []string{"Every other Friday", "visions", "a missing ledger"} {
			if !strings.Contains(body, want) {
				t.Errorf("%s should see %q on the campaign page: %s", who, want, body)
			}
		}
	}
	_, anaBody := st.get("Ana", "/c/1")
	if strings.Contains(anaBody, `href="/c/1/settings"`) {
		t.Error("a player shouldn't see the Edit link to Settings")
	}
	_, seerBody := st.get("Seer", "/c/1")
	if !strings.Contains(seerBody, `href="/c/1/settings"`) {
		t.Error("the Seer should see the Edit link to Settings")
	}
}
