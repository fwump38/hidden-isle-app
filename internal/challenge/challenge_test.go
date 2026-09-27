package challenge

import (
	"strings"
	"testing"
)

func card(t *testing.T, s string) Card {
	t.Helper()
	c, err := ParseCard(s)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// The book's worked examples (pp. 16, 19, 21).
func TestResolveBookExamples(t *testing.T) {
	cases := []struct {
		name         string
		trump        string
		played, seer string
		fortunes     []Fortune
		want         string
	}{
		{"p16 trump vs non-trump", "Swords", "3 of Swords", "10 of Pentacles", nil, "total"},
		{"p16 higher non-trump", "Swords", "Ace of Wands", "10 of Pentacles", nil, "complicated"},
		{"p16 lower non-trump", "Swords", "3 of Wands", "10 of Pentacles", nil, "failure"},
		{"seer trump", "Swords", "10 of Cups", "2 of Swords", nil, "failure"},
		{"both trump, tie", "Cups", "7 of Cups", "7 of Cups", nil, "complicated"},
		{"trump ace beats trump ten", "Wands", "Ace of Wands", "10 of Wands", nil, "total"},
		{"p19 two fortunes to a trump tie", "Swords", "5 of Pentacles", "8 of Swords", []Fortune{
			{Card: Card{"Swords", 2}, Mode: "suit", By: "Anton"}, {Card: Card{"Wands", 3}, Mode: "add", By: "Sarah"}}, "complicated"},
		{"p19 suit bonus", "Swords", "4 of Wands", "5 of Swords", []Fortune{
			{Card: Card{"Swords", 2}, Mode: "suit", SuitBonus: true, By: "Eldon"}}, "complicated"},
	}
	for _, tc := range cases {
		r, err := Resolve(tc.trump, card(t, tc.played), card(t, tc.seer), 0, tc.fortunes)
		if err != nil {
			t.Fatal(err)
		}
		if r.Outcome != tc.want {
			t.Errorf("%s: got %s, want %s\n%s", tc.name, r.Outcome, tc.want, strings.Join(r.Steps, "\n"))
		}
	}
	// p. 19: 5 of Pentacles → 5 of Swords → 8 of Swords.
	r, _ := Resolve("Swords", card(t, "5 of Pentacles"), card(t, "8 of Swords"), 0,
		[]Fortune{{Card: Card{"Swords", 2}, Mode: "suit"}, {Card: Card{"Wands", 3}, Mode: "add"}})
	if r.Challenge != "5 of Swords" || r.ChallengeN != 8 {
		t.Errorf("p19 card = %s (%d)", r.Challenge, r.ChallengeN)
	}
	// p. 19 suit bonus: 4 of Wands becomes the 7 of Swords.
	r, _ = Resolve("Swords", card(t, "4 of Wands"), card(t, "5 of Swords"), 0, []Fortune{{Card: Card{"Swords", 2}, Mode: "suit", SuitBonus: true}})
	if r.ChallengeN != 7 {
		t.Errorf("suit bonus number = %d, want 7", r.ChallengeN)
	}
}

func TestIdealBonusAndAceFortuneNote(t *testing.T) {
	r, _ := Resolve("Cups", card(t, "6 of Cups"), card(t, "8 of Cups"), 3, nil)
	if r.Outcome != "complicated" || r.ChallengeN != 9 {
		t.Errorf("ideal +3: %s %d", r.Outcome, r.ChallengeN)
	}
	r, _ = Resolve("Cups", card(t, "Ace of Wands"), card(t, "9 of Cups"), 0, []Fortune{{Card: Card{"Cups", 4}, Mode: "suit"}})
	if r.Outcome != "total" || len(r.Notes) == 0 {
		t.Errorf("fortune-made trump Ace should be total with a note: %s %v", r.Outcome, r.Notes)
	}
}

func TestCounts(t *testing.T) {
	c, err := Counts(Setup{Skill: "Skirmish", SkillPoints: 2, Difficulty: "medium", SeerExtra: 1})
	if err != nil || c.AgentCards != 3 || c.SeerCards != 4 || c.Trump != "Swords" {
		t.Errorf("basic: %+v %v", c, err)
	}
	c, _ = Counts(Setup{Skill: "Slip", SkillPoints: 0, HarmInSuit: 2, Ideal: true, Difficulty: "hard"})
	if c.AgentCards != 1 || c.NumeralBonus != 3 || !c.MarkIdeal || !strings.Contains(strings.Join(c.Breakdown, " "), "minimum 1") {
		t.Errorf("minimum one card: %+v", c)
	}
	c, _ = Counts(Setup{Skill: "Channel", SkillPoints: 1, Burden: true, Vice: true, Modifiers: []Modifier{{"item", 1}}, Difficulty: "easy", Participants: 3})
	if c.AgentCards != 5 || !c.MarkBurden || c.SeerCards != 5 {
		t.Errorf("traits and group: %+v", c)
	}
	c, _ = Counts(Setup{Skill: "Unleash", SkillPoints: 1, HarmInSuit: 2, IgnoreHarm: true, Difficulty: "easy"})
	if c.AgentCards != 2 {
		t.Errorf("ignore harm: %d", c.AgentCards)
	}
	if _, err := Counts(Setup{Skill: "Juggling", Difficulty: "easy"}); err == nil {
		t.Error("unknown skill accepted")
	}
	if _, err := ParseCard("Queen of Cups"); err == nil {
		t.Error("court card accepted as a pip")
	}
}
