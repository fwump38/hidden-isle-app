package web

import (
	"encoding/json"
	"net/url"
	"testing"

	"github.com/fwump38/hidden-isle-app/internal/db"
)

func TestRecordPatch(t *testing.T) {
	c := &db.Clock{Name: "Guards arrive", Segments: 4, Filled: 1}
	p, err := recordPatch(c, url.Values{"set.name": {"Guards  arrive "}, "inc.filled": {"2"}, "set.visibility": {"seer"}, "why": {"x"}})
	if err != nil {
		t.Fatal(err)
	}
	if string(p["filled"]) != "3" || string(p["name"]) != `"Guards  arrive"` || string(p["visibility"]) != `"seer"` || len(p) != 3 {
		t.Errorf("patch = %v", p)
	}
	if _, err := recordPatch(c, url.Values{"set.nonsense": {"1"}}); err == nil {
		t.Error("unknown field accepted")
	}
	a := &db.Adversary{}
	p, _ = recordPatch(a, url.Values{"set.hidden": {"", "on"}, "set.major": {""}})
	if string(p["hidden"]) != "true" || string(p["major"]) != "false" {
		t.Errorf("checkboxes: %v", p)
	}
	ag := &db.Agent{}
	p, _ = recordPatch(ag, url.Values{"set.vices": {"Gambling\nDrink\n"}, "set.owner_id": {""}})
	if string(p["vices"]) != `["Gambling","Drink"]` || string(p["owner_id"]) != "null" {
		t.Errorf("list/pointer: %v", p)
	}
}

func TestAgentPatch(t *testing.T) {
	ag := &db.Agent{Skills: map[string]int{"Slip": 1, "Finesse": 1}, Harm: map[string][]string{"Cups": {"P"}},
		Abilities: []db.AgentAbility{{ID: "wisp"}}, LoadUsed: 1, Items: []db.AgentItem{{Name: "Rope", Used: true}}}
	p, err := agentPatch(ag, url.Values{
		"skill.Slip": {"2"}, "harm.Cups.0": {"P"}, "harm.Cups.1": {"S"}, "harm.Wands.0": {""}, "harm.Wands.1": {""},
		"item.toggle": {"Rope"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var skills map[string]int
	json.Unmarshal(p["skills"], &skills)
	if skills["Slip"] != 2 || skills["Finesse"] != 1 {
		t.Errorf("skills = %v (must keep other skills)", skills)
	}
	var harm map[string][]string
	json.Unmarshal(p["harm"], &harm)
	if len(harm["Cups"]) != 2 || harm["Cups"][1] != "S" || len(harm["Wands"]) != 0 {
		t.Errorf("harm = %v", harm)
	}
	if string(p["load_used"]) != "0" || string(p["items"]) != "[]" && string(p["items"]) != "null" {
		t.Errorf("putting back an item should free its load: %s %s", p["items"], p["load_used"])
	}
	if ag.Skills["Slip"] != 1 || len(ag.Harm["Cups"]) != 1 {
		t.Error("agentPatch must not modify the Agent")
	}
}
