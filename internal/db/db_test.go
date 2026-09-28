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

// TestMergeNoteEntriesMigration seeds entries the way they looked before the "note" kind was
// removed, then re-runs the migration against them (simulating an upgrade from an older
// database) and checks each ends up where Phase 4 says it should.
func TestMergeNoteEntriesMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "notes.db")
	g, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}

	seed := []*Entry{
		{CampaignID: 1, AuthorID: 1, Kind: "note", Visibility: VisSeer, Title: "Twist", Body: "The mayor is the leader"},
		{CampaignID: 1, AuthorID: 2, Kind: "note", Visibility: VisOwner, Body: "remember to buy rope"},
		{CampaignID: 1, AuthorID: 2, Kind: "journal", Visibility: VisOwner, Title: "Diary", Body: "x"},
		{CampaignID: 1, AuthorID: 2, Kind: "journal", Visibility: VisPrivate, Title: "Secret", Body: "y"},
		{CampaignID: 1, AuthorID: 1, Kind: "journal", Visibility: VisParty, Title: "Draft recap", Body: "z"},
	}
	for _, e := range seed {
		if err := g.Create(e).Error; err != nil {
			t.Fatal(err)
		}
	}
	// Undo the migration's own bookkeeping so it re-runs against this seeded data, as it would on
	// a real database that predates it.
	if err := g.Where("id = ?", "2025_merge_note_entries").Delete(&SchemaMigration{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := Migrate(g); err != nil {
		t.Fatal(err)
	}

	var notes []SeerNote
	g.Where("campaign_id = ?", 1).Find(&notes)
	if len(notes) != 1 || notes[0].Title != "Twist" || notes[0].Body != "The mayor is the leader" {
		t.Fatalf("Seer-visibility note should become a SeerNote: %+v", notes)
	}

	var remaining []Entry
	g.Order("id").Find(&remaining)
	if len(remaining) != 4 {
		t.Fatalf("entries left = %d, want 4 (the Seer-visibility note is gone): %+v", len(remaining), remaining)
	}
	byBody := map[string]Entry{}
	for _, e := range remaining {
		if e.Kind != "journal" {
			t.Errorf("entry %q kind = %q, want journal (the note kind is gone)", e.Body, e.Kind)
		}
		byBody[e.Body] = e
	}
	for _, want := range []string{"remember to buy rope", "x", "y"} {
		if e, ok := byBody[want]; !ok || !e.Published {
			t.Errorf("non-party entry %q should be published: %+v", want, e)
		}
	}
	if e, ok := byBody["z"]; !ok || e.Published {
		t.Errorf("party-visibility draft should stay unpublished: %+v", e)
	}
}
