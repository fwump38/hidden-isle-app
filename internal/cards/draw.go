// Package cards draws from the two decks with OS randomness, like the rules repo's draw.py.
// The table normally draws real cards; this is the digital backup.
package cards

import (
	"crypto/rand"
	"fmt"
	"math/big"

	"github.com/fwump38/hidden-isle-app/internal/gamedata"
)

// Hand is one named hand of drawn card names.
type Hand struct {
	Name  string   `json:"name,omitempty"`
	Cards []string `json:"cards"`
}

// Request asks for Count cards into a hand called Name ("" for an unnamed hand).
type Request struct {
	Name  string `json:"name,omitempty"`
	Count int    `json:"count"`
}

// Draw deals the hands from one freshly shuffled deck ("vision" or "pips"), so no card appears
// in two hands. Every call starts from a full deck: discards are shuffled back after each use (p. 9).
func Draw(snap *gamedata.Snapshot, deck string, reqs []Request) ([]Hand, error) {
	var names []string
	switch deck {
	case "vision":
		for _, c := range snap.Cards.Vision {
			names = append(names, c.Name)
		}
	case "pips":
		for _, c := range snap.Cards.Pips {
			names = append(names, c.Name)
		}
	default:
		return nil, fmt.Errorf("unknown deck %q (use vision or pips)", deck)
	}
	total := 0
	for _, r := range reqs {
		if r.Count < 1 {
			return nil, fmt.Errorf("hand %q: count must be at least 1", r.Name)
		}
		total += r.Count
	}
	if total > len(names) {
		return nil, fmt.Errorf("the %s deck only has %d cards", deck, len(names))
	}
	if err := shuffle(names); err != nil {
		return nil, err
	}
	hands := make([]Hand, 0, len(reqs))
	for _, r := range reqs {
		hands = append(hands, Hand{Name: r.Name, Cards: append([]string(nil), names[:r.Count]...)})
		names = names[r.Count:]
	}
	return hands, nil
}

// shuffle is a Fisher-Yates shuffle using crypto/rand.
func shuffle(s []string) error {
	for i := len(s) - 1; i > 0; i-- {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(i+1)))
		if err != nil {
			return err
		}
		j := int(n.Int64())
		s[i], s[j] = s[j], s[i]
	}
	return nil
}
