package oracle

import (
	"os"
	"testing"

	"github.com/fwump38/hidden-isle-app/internal/challenge"
	"github.com/fwump38/hidden-isle-app/internal/gamedata"
)

func c(t *testing.T, s string) challenge.Card {
	t.Helper()
	x, err := challenge.ParseCard(s)
	if err != nil {
		t.Fatal(err)
	}
	return x
}

func tables(t *testing.T) (*Tables, *gamedata.Snapshot) {
	t.Helper()
	src := os.Getenv("HI_TEST_GAMEDATA")
	if src == "" {
		t.Skip("HI_TEST_GAMEDATA not set")
	}
	snap, err := gamedata.Load(src, "test")
	if err != nil {
		t.Fatal(err)
	}
	tb, err := Load(snap)
	if err != nil {
		t.Fatal(err)
	}
	return tb, snap
}

func TestNumeric(t *testing.T) {
	for _, tc := range []struct {
		rng, card string
		want      int
		event     bool
	}{{"1-5", "7 of Cups", 4, false}, {"1-10", "Ace of Swords", 1, true}, {"2-20", "9 of Wands", 18, false}, {"10-100", "3 of Cups", 30, false}} {
		r, err := Numeric(tc.rng, c(t, tc.card))
		if err != nil || r.Number != tc.want || r.RandomEvent != tc.event {
			t.Errorf("%s %s: %+v %v", tc.rng, tc.card, r, err)
		}
	}
}

func TestOracleWithRules(t *testing.T) {
	tb, snap := tables(t)
	y, n, err := tb.Hands("likely")
	if err != nil || y != 2 || n != 1 {
		t.Fatalf("likely hands %d/%d %v", y, n, err)
	}
	r, _ := tb.Closed("likely", []challenge.Card{c(t, "3 of Cups"), c(t, "8 of Swords")}, []challenge.Card{c(t, "8 of Wands")}, true)
	if r.Answer != "Yes" || r.Extreme {
		t.Errorf("tie goes to yes, suits differ: %+v", r)
	}
	r, _ = tb.Closed("50-50", []challenge.Card{c(t, "4 of Cups")}, []challenge.Card{c(t, "9 of Cups")}, true)
	if r.Answer != "No" || !r.Extreme {
		t.Errorf("matching suits → extreme no: %+v", r)
	}
	r, _ = tb.Closed("50-50", []challenge.Card{c(t, "Ace of Cups")}, []challenge.Card{c(t, "9 of Wands")}, false)
	if r.Answer != "No" || !r.RandomEvent {
		t.Errorf("Ace low loses, triggers event: %+v", r)
	}

	e := tb.RandomEvent(c(t, "6 of Swords"))
	if e.When != "present" || len(e.Options) != 2 || e.Options[0].Ideas == "" {
		t.Errorf("event: %+v", e)
	}
	if e := tb.RandomEvent(c(t, "Ace of Cups")); e.When != "future" {
		t.Errorf("Ace is future (9-A): %s", e.When)
	}

	v, err := FindVision(snap, "The Tower")
	if err != nil {
		t.Fatal(err)
	}
	m := c(t, "Ace of Pentacles")
	npc := tb.MakeNPC(v, &m, "Venice")
	if npc.Method == "" || !npc.RandomEvent || len(npc.Names) != 4 || npc.NamesFrom == "" {
		t.Errorf("npc: %+v", npc)
	}
	for _, tc := range []struct{ card, want string }{{"2 of Cups", "Retrieve a text, map or artifact"}, {"5 of Wands", "Retrieve or rescue a person"}, {"Ace of Swords", "Prevent a disaster (assassination, ritual…)"}, {"10 of Cups", "Sabotage a stronghold or facility"}} {
		ms, err := tb.MissionType(c(t, tc.card))
		if err != nil || ms.MissionType != tc.want {
			t.Errorf("mission %s: %+v %v", tc.card, ms, err)
		}
	}
}
