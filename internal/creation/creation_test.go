package creation

import (
	"strings"
	"testing"

	"github.com/fwump38/hidden-isle-app/internal/gamedata"
)

func TestGenerateProducesARulesLegalAgent(t *testing.T) {
	snap := gamedata.Fixture()
	class := snap.Class("occultist") // StartsWithAdeptProficiency: true
	for i := 0; i < 20; i++ {
		res, err := Generate(snap, class, false)
		if err != nil {
			t.Fatalf("Generate: %v", err)
		}
		for _, f := range []string{"child_card", "child_phrase", "adult_card", "adult_phrase", "adult_verb",
			"burden_card", "burden", "ideal_card", "ideal", "abilities", "skills", "name", "age", "culture", "look", "why"} {
			if _, ok := res.Fields[f]; !ok {
				t.Errorf("missing field %q", f)
			}
		}
		if v := res.Fields["adult_verb"]; v != "survived" && v != "flourished" {
			t.Errorf("adult_verb = %v", v)
		}
		abs, ok := res.Fields["abilities"].([]map[string]string)
		if !ok || len(abs) != snap.Limits.Creation.Abilities {
			t.Fatalf("abilities = %#v, want %d entries", res.Fields["abilities"], snap.Limits.Creation.Abilities)
		}
		seen := map[string]bool{}
		for _, ab := range abs {
			if seen[ab["id"]] {
				t.Errorf("duplicate ability %q", ab["id"])
			}
			seen[ab["id"]] = true
		}
		if _, ok := res.Fields["proficiencies"]; !ok {
			t.Errorf("occultist should get a magical proficiency")
		}

		skills, ok := res.Fields["skills"].(map[string]int)
		if !ok {
			t.Fatalf("skills = %#v", res.Fields["skills"])
		}
		total := 0
		for sk, v := range skills {
			total += v
			if v < 0 || v > snap.Limits.Creation.MaxSkill {
				t.Errorf("skill %s = %d, want 0-%d at creation", sk, v, snap.Limits.Creation.MaxSkill)
			}
			found := false
			for _, s := range snap.Skills.Skills {
				if s.Name == sk {
					found = true
				}
			}
			if !found {
				t.Errorf("unknown skill %q", sk)
			}
		}
		if total != snap.Limits.Creation.TotalPoints {
			t.Errorf("skill total = %d, want %d", total, snap.Limits.Creation.TotalPoints)
		}

		if len(res.Contacts) != 2 {
			t.Fatalf("contacts = %d, want 2", len(res.Contacts))
		}
		kinds := map[string]bool{}
		for _, c := range res.Contacts {
			kinds[c.Kind] = true
			if c.Name == "" || c.Card == "" {
				t.Errorf("contact %+v missing name or card", c)
			}
		}
		if !kinds["Homeland"] || !kinds["Dioscorian"] {
			t.Errorf("contacts = %+v, want one Homeland and one Dioscorian", res.Contacts)
		}
		for _, c := range res.Contacts {
			if c.Kind == "Dioscorian" && c.Affection != snap.Limits.Creation.DioscorianContactAffection {
				t.Errorf("Dioscorian affection = %d, want %d", c.Affection, snap.Limits.Creation.DioscorianContactAffection)
			}
		}
		if len(res.Log) == 0 {
			t.Error("expected a review log")
		}
	}
}

func TestGenerateSoloAllowsHigherSkillsWithinLimit(t *testing.T) {
	snap := gamedata.Fixture()
	class := snap.Class("prowler")
	sawThree := false
	for i := 0; i < 30; i++ {
		res, err := Generate(snap, class, true)
		if err != nil {
			t.Fatalf("Generate: %v", err)
		}
		skills := res.Fields["skills"].(map[string]int)
		total, atThree := 0, 0
		for sk, v := range skills {
			total += v
			if v > snap.Limits.Creation.Solo.MaxSkill {
				t.Errorf("solo skill %s = %d, want at most %d", sk, v, snap.Limits.Creation.Solo.MaxSkill)
			}
			if v == snap.Limits.Creation.Solo.MaxSkill {
				atThree++
			}
		}
		if atThree > snap.Limits.Creation.Solo.MaxSkillsAt3 {
			t.Errorf("skills at %d: %d, want at most %d", snap.Limits.Creation.Solo.MaxSkill, atThree, snap.Limits.Creation.Solo.MaxSkillsAt3)
		}
		if atThree > 0 {
			sawThree = true
		}
		if total != snap.Limits.Creation.Solo.TotalPoints {
			t.Errorf("solo skill total = %d, want %d", total, snap.Limits.Creation.Solo.TotalPoints)
		}
	}
	if !sawThree {
		t.Error("expected at least one run to push a skill to the solo max across 30 tries")
	}
}

func TestGenerateNativeBornUsesDioscoriaNotBookRegion(t *testing.T) {
	snap := gamedata.Fixture()
	class := snap.Class("prowler")
	for i := 0; i < 50; i++ {
		res, err := Generate(snap, class, false)
		if err != nil {
			t.Fatal(err)
		}
		why := res.Fields["why"].(string)
		if strings.HasPrefix(why, "Native-born") {
			if res.Fields["culture"] != "Dioscorian" {
				t.Errorf("native-born culture = %v, want Dioscorian", res.Fields["culture"])
			}
			return
		}
	}
	t.Skip("never drew the native-born reason in 50 tries (random)")
}

func TestGenerateUsesHomebrewSurnames(t *testing.T) {
	snap := gamedata.Fixture()
	class := snap.Class("prowler")
	sawSurname := false
	for i := 0; i < 20; i++ {
		res, err := Generate(snap, class, false)
		if err != nil {
			t.Fatal(err)
		}
		name := res.Fields["name"].(string)
		if strings.Contains(name, "Testfam") || strings.Contains(name, "Othertest") || strings.Contains(name, "of House Test") {
			sawSurname = true
		}
	}
	if !sawSurname {
		t.Error("expected at least one generated name to use a homebrew surname/byname across 20 tries")
	}
}

func TestGenerateRequiresClassAndSnapshot(t *testing.T) {
	snap := gamedata.Fixture()
	if _, err := Generate(snap, nil, false); err == nil {
		t.Error("expected an error with no class")
	}
	if _, err := Generate(nil, snap.Class("prowler"), false); err == nil {
		t.Error("expected an error with no snapshot")
	}
}

// TestGenerateFillsInAContactGrantingAbility: an ability that creates a contact (The Old Ways,
// Celestial Bargain, p. 43-45) mustn't be left for the player to notice and fill in by hand — the
// generator should pick one for it, named and described, ready for the caller to save.
func TestGenerateFillsInAContactGrantingAbility(t *testing.T) {
	snap := gamedata.Fixture()
	class := snap.Class("occultist")
	class.Abilities[0].ID = "the-old-ways"
	class.Abilities[0].Name = "THE OLD WAYS"

	res, err := Generate(snap, class, false)
	if err != nil {
		t.Fatal(err)
	}
	c, ok := res.AbilityContacts["the-old-ways"]
	if !ok {
		t.Fatal("expected a generated contact for the-old-ways")
	}
	if c.Name == "" || c.Kind != "Deity (The Old Ways)" || c.Card == "" {
		t.Errorf("generated contact = %+v", c)
	}
	found := false
	for _, line := range res.Log {
		if strings.Contains(line, "grants a contact") {
			found = true
		}
	}
	if !found {
		t.Error("expected the log to mention the granted contact")
	}
	// An ability with no contact grant shouldn't get an entry.
	if _, ok := res.AbilityContacts["familiar"]; ok {
		t.Error("familiar doesn't grant a contact")
	}
}
