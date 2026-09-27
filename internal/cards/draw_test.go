package cards

import (
	"fmt"
	"testing"

	"github.com/fwump38/hidden-isle-app/internal/gamedata"
)

func testSnap() *gamedata.Snapshot {
	s := &gamedata.Snapshot{}
	for i := 0; i < 38; i++ {
		s.Cards.Vision = append(s.Cards.Vision, gamedata.VisionCard{Name: fmt.Sprintf("V%d", i)})
	}
	for i := 0; i < 40; i++ {
		s.Cards.Pips = append(s.Cards.Pips, gamedata.PipCard{Name: fmt.Sprintf("P%d", i)})
	}
	return s
}

func TestDrawNoCardInTwoHands(t *testing.T) {
	s := testSnap()
	for n := 0; n < 200; n++ {
		hands, err := Draw(s, "pips", []Request{{"agent", 5}, {"seer", 6}, {"x", 9}})
		if err != nil {
			t.Fatal(err)
		}
		seen := map[string]bool{}
		for _, h := range hands {
			for _, c := range h.Cards {
				if seen[c] {
					t.Fatalf("card %s dealt twice: %+v", c, hands)
				}
				seen[c] = true
			}
		}
		if len(seen) != 20 {
			t.Fatalf("dealt %d cards, want 20", len(seen))
		}
	}
}

func TestDrawLimits(t *testing.T) {
	s := testSnap()
	if _, err := Draw(s, "vision", []Request{{"", 39}}); err == nil {
		t.Error("drew 39 vision cards from a 38-card deck")
	}
	if _, err := Draw(s, "pips", []Request{{"", 0}}); err == nil {
		t.Error("accepted a zero-card hand")
	}
	if _, err := Draw(s, "tarot", []Request{{"", 1}}); err == nil {
		t.Error("accepted an unknown deck")
	}
	if h, err := Draw(s, "vision", []Request{{"", 38}}); err != nil || len(h[0].Cards) != 38 {
		t.Errorf("full vision deck: %v %v", h, err)
	}
}
