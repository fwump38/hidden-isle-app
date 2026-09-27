package db

import (
	"path/filepath"
	"testing"
)

func TestMigrateTwice(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.db")
	g, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	a := Agent{CampaignID: 1, Name: "Ines", Class: "prowler", Skills: map[string]int{"Slip": 2},
		Harm: map[string][]string{"Cups": {"P"}}, Abilities: []AgentAbility{{ID: "wisp"}}}
	if err := g.Create(&a).Error; err != nil {
		t.Fatal(err)
	}
	if err := Migrate(g); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	var got Agent
	g.First(&got, a.ID)
	if got.Skills["Slip"] != 2 || got.Harm["Cups"][0] != "P" || got.Abilities[0].ID != "wisp" {
		t.Fatalf("json columns didn't round-trip: %+v", got)
	}
}
