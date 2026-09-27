// Package oracle reads fate questions, random events, NPCs, complications and mission types
// from cards (Rulebook pp. 72, 100-103), using the tables in the rules data. Like the challenge
// package it never picks cards: they come from the table or an explicit digital draw.
package oracle

import (
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/fwump38/hidden-isle-app/internal/challenge"
	"github.com/fwump38/hidden-isle-app/internal/gamedata"
)

type Tables struct {
	MissionTypes struct {
		Page string `yaml:"page"`
		Rows []struct {
			Cards        string `yaml:"cards" json:"cards"`
			MissionType  string `yaml:"mission_type" json:"mission_type"`
			GoalQuestion string `yaml:"goal_question" json:"goal_question"`
		} `yaml:"rows"`
	} `yaml:"mission_types"`
	FateClosed struct {
		Page  string                    `yaml:"page"`
		Hands map[string]map[string]int `yaml:"hands"`
	} `yaml:"fate_closed"`
	RandomEvents struct {
		Page       string              `yaml:"page"`
		SuitThemes map[string][]string `yaml:"suit_themes"`
		Rows       []struct {
			Theme   string `yaml:"theme"`
			Past    string `yaml:"past"`
			Present string `yaml:"present"`
			Future  string `yaml:"future"`
		} `yaml:"rows"`
	} `yaml:"random_events"`
	NPCMethods struct {
		Page string `yaml:"page"`
		Rows []struct {
			Suit   string `yaml:"suit"`
			Method string `yaml:"method"`
		} `yaml:"rows"`
	} `yaml:"npc_methods"`
	Names []struct {
		Region string   `yaml:"region"`
		Page   string   `yaml:"page"`
		Names  []string `yaml:"names"`
	} `yaml:"-"`
}

// Load reads the oracle and name tables out of a snapshot.
func Load(snap *gamedata.Snapshot) (*Tables, error) {
	if snap == nil {
		return nil, errors.New("rules data isn't loaded")
	}
	var t Tables
	if err := remarshal(snap.Raw["oracle"], &t); err != nil {
		return nil, fmt.Errorf("oracle tables: %w", err)
	}
	var set struct {
		Names []struct {
			Region string   `yaml:"region"`
			Page   string   `yaml:"page"`
			Names  []string `yaml:"names"`
		} `yaml:"names"`
	}
	if err := remarshal(snap.Raw["setting"], &set); err != nil {
		return nil, fmt.Errorf("setting tables: %w", err)
	}
	for _, n := range set.Names {
		t.Names = append(t.Names, n)
	}
	return &t, nil
}

func remarshal(in any, out any) error {
	b, err := yaml.Marshal(in)
	if err != nil {
		return err
	}
	return yaml.Unmarshal(b, out)
}

// ---------------------------------------------------------------- fate questions

type FateResult struct {
	Answer      string   `json:"answer"`
	Extreme     bool     `json:"extreme,omitempty"`
	RandomEvent bool     `json:"random_event,omitempty"`
	Number      int      `json:"number,omitempty"`
	Steps       []string `json:"steps"`
}

// Hands returns how many cards the yes and no hands get for a likelihood (p. 101).
func (t *Tables) Hands(likelihood string) (yes, no int, err error) {
	h, ok := t.FateClosed.Hands[likelihood]
	if !ok {
		return 0, 0, errors.New("likelihood must be unlikely, 50-50 or likely")
	}
	return h["yes"], h["no"], nil
}

// Closed answers a yes/no question (pp. 100-101): keep the highest card in each hand; yes wins
// if its card is at least no's. Matching suits make it extreme; any Ace triggers a random event.
// The book doesn't give the Ace's value here: aceHigh true counts it as 11 (as in challenges),
// false as 1 (as for fate numbers). That's a Seer ruling to record once.
func (t *Tables) Closed(likelihood string, yes, no []challenge.Card, aceHigh bool) (FateResult, error) {
	wy, wn, err := t.Hands(likelihood)
	if err != nil {
		return FateResult{}, err
	}
	if len(yes) == 0 || len(no) == 0 {
		return FateResult{}, errors.New("both hands need at least one card")
	}
	var r FateResult
	if len(yes) != wy || len(no) != wn {
		r.Steps = append(r.Steps, fmt.Sprintf("Note: %s is %d yes / %d no cards (p. 101); these hands have %d / %d.", likelihood, wy, wn, len(yes), len(no)))
	}
	val := func(c challenge.Card) int {
		if c.Rank == 1 {
			if aceHigh {
				return 11
			}
			return 1
		}
		return c.Rank
	}
	best := func(h []challenge.Card) challenge.Card {
		b := h[0]
		for _, c := range h[1:] {
			if val(c) > val(b) {
				b = c
			}
		}
		return b
	}
	by, bn := best(yes), best(no)
	r.Steps = append(r.Steps, fmt.Sprintf("Highest yes: %s (%d). Highest no: %s (%d).", by, val(by), bn, val(bn)))
	if val(by) >= val(bn) {
		r.Answer = "Yes"
		r.Steps = append(r.Steps, "Yes wins ties (p. 101).")
	} else {
		r.Answer = "No"
	}
	if by.Suit == bn.Suit {
		r.Extreme = true
		r.Steps = append(r.Steps, "Matching suits: an extreme answer. Wanted → an advantage; unwanted → a complication (p. 101).")
	}
	for _, c := range append(append([]challenge.Card{}, yes...), no...) {
		if c.Rank == 1 {
			r.RandomEvent = true
		}
	}
	if r.RandomEvent {
		r.Steps = append(r.Steps, "An Ace was drawn: a random event happens too (p. 101).")
	}
	return r, nil
}

// Numeric turns one pip into a number for a range (p. 101). Ace = 1, and triggers a random event.
func Numeric(rng string, c challenge.Card) (FateResult, error) {
	v := c.Rank // Ace = 1 here
	r := FateResult{Steps: []string{fmt.Sprintf("%s = %d (Ace = 1 for fate numbers, p. 100).", c, v)}}
	switch rng {
	case "1-5":
		r.Number = (v + 1) / 2
		r.Steps = append(r.Steps, "1-5: half, rounded up.")
	case "1-10":
		r.Number = v
	case "2-20":
		r.Number = v * 2
		r.Steps = append(r.Steps, "2-20: ×2.")
	case "10-100":
		r.Number = v * 10
		r.Steps = append(r.Steps, "10-100: ×10.")
	default:
		return r, errors.New("range must be 1-5, 1-10, 2-20 or 10-100")
	}
	r.Answer = fmt.Sprint(r.Number)
	if c.Rank == 1 {
		r.RandomEvent = true
		r.Steps = append(r.Steps, "An Ace: a random event happens too.")
	}
	return r, nil
}

// ---------------------------------------------------------------- random events

type EventOption struct {
	Theme string `json:"theme"`
	Ideas string `json:"ideas"`
}

type Event struct {
	Card    string        `json:"card"`
	When    string        `json:"when"`
	Options []EventOption `json:"options"`
	Steps   []string      `json:"steps"`
}

// RandomEvent reads one pip (p. 102): the number says when (2-4 past evidence, 5-8 happening now,
// 9-Ace a plan or seed), the suit offers two themes; pick the one that fits the scene.
func (t *Tables) RandomEvent(c challenge.Card) Event {
	e := Event{Card: c.String()}
	switch {
	case c.Rank == 1 || c.Rank >= 9:
		e.When = "future"
	case c.Rank >= 5:
		e.When = "present"
	default:
		e.When = "past"
	}
	desc := map[string]string{"past": "past: evidence of something that happened", "present": "present: it's happening now", "future": "future: a plan or a seed"}
	e.Steps = append(e.Steps, fmt.Sprintf("%s: %s (p. 102).", c, desc[e.When]))
	for _, th := range t.RandomEvents.SuitThemes[c.Suit] {
		for _, row := range t.RandomEvents.Rows {
			if strings.EqualFold(row.Theme, th) {
				ideas := map[string]string{"past": row.Past, "present": row.Present, "future": row.Future}[e.When]
				e.Options = append(e.Options, EventOption{Theme: row.Theme, Ideas: ideas})
			}
		}
	}
	e.Steps = append(e.Steps, fmt.Sprintf("%s offers two themes; pick the one that fits the scene.", c.Suit))
	return e
}

// ---------------------------------------------------------------- NPCs, complications, missions

type NPC struct {
	Card        string   `json:"card"`
	Characters  string   `json:"characters"`
	Meaning     string   `json:"meaning"`
	Method      string   `json:"method,omitempty"`
	MethodCard  string   `json:"method_card,omitempty"`
	Names       []string `json:"name_ideas,omitempty"`
	NamesFrom   string   `json:"names_from,omitempty"`
	RandomEvent bool     `json:"random_event,omitempty"`
	Steps       []string `json:"steps"`
}

// MakeNPC reads an NPC (p. 103): a vision card for who they are, a pip for their method (an Ace
// also triggers a random event), and name ideas from the region's list.
func (t *Tables) MakeNPC(v gamedata.VisionCard, method *challenge.Card, region string) NPC {
	n := NPC{Card: v.Name, Characters: v.Characters, Meaning: v.Meaning}
	n.Steps = append(n.Steps, fmt.Sprintf("%s: characters %s; %s (Vision Guide).", v.Name, v.Characters, v.Meaning))
	if method != nil {
		n.MethodCard = method.String()
		for _, row := range t.NPCMethods.Rows {
			if row.Suit == method.Suit {
				n.Method = row.Method
			}
		}
		n.Steps = append(n.Steps, fmt.Sprintf("Method from %s: %s (p. 103).", method, n.Method))
		if method.Rank == 1 {
			n.RandomEvent = true
			n.Steps = append(n.Steps, "An Ace: finish the NPC, then resolve a random event.")
		}
	}
	for _, nl := range t.Names {
		if region == "" || strings.Contains(strings.ToLower(nl.Region), strings.ToLower(region)) {
			n.Names = pick(nl.Names, 4)
			n.NamesFrom = nl.Region + " (" + nl.Page + ")"
			break
		}
	}
	return n
}

// Regions lists the regions with name lists.
func (t *Tables) Regions() []string {
	var out []string
	for _, n := range t.Names {
		out = append(out, n.Region)
	}
	return out
}

type Complication struct {
	Card        string   `json:"card"`
	BadOutcomes string   `json:"bad_outcomes"`
	Groups      string   `json:"groups_and_creatures"`
	Steps       []string `json:"steps"`
}

// MakeComplication reads a vision card for complications (p. 18, p. 91).
func MakeComplication(v gamedata.VisionCard) Complication {
	return Complication{Card: v.Name, BadOutcomes: v.BadOutcomes, Groups: v.GroupsAndCreatures,
		Steps: []string{"Offer 2-3 complications from the bad outcomes and groups & creatures, fitted to the scene (p. 18, p. 91); mark which hit harder."}}
}

type Mission struct {
	Card         string `json:"card"`
	MissionType  string `json:"mission_type"`
	GoalQuestion string `json:"goal_question"`
	Page         string `json:"page"`
}

// MissionType reads one pip for the mission type (p. 72).
func (t *Tables) MissionType(c challenge.Card) (Mission, error) {
	key := fmt.Sprint(c.Rank)
	if c.Rank == 1 {
		key = "A"
	}
	for _, row := range t.MissionTypes.Rows {
		lo, hi, _ := strings.Cut(row.Cards, "-")
		if hi == "" {
			hi = lo
		}
		if key == lo || key == hi || (key != "A" && lo != "A" && atoi(lo) <= c.Rank && c.Rank <= atoi(hi)) {
			return Mission{Card: c.String(), MissionType: row.MissionType, GoalQuestion: row.GoalQuestion, Page: t.MissionTypes.Page}, nil
		}
	}
	return Mission{}, fmt.Errorf("no mission type for %s", c)
}

func atoi(s string) int {
	n := 0
	fmt.Sscan(s, &n)
	return n
}

// pick returns up to n different random entries (name suggestions, not card draws).
func pick(list []string, n int) []string {
	idx := make([]int, len(list))
	for i := range idx {
		idx[i] = i
	}
	var out []string
	for i := 0; i < n && i < len(idx); i++ {
		j, _ := rand.Int(rand.Reader, big.NewInt(int64(len(idx)-i)))
		k := i + int(j.Int64())
		idx[i], idx[k] = idx[k], idx[i]
		out = append(out, list[idx[i]])
	}
	return out
}

// FindVision looks up a vision card by name, numeral or id.
func FindVision(snap *gamedata.Snapshot, name string) (gamedata.VisionCard, error) {
	q := strings.ToLower(strings.TrimSpace(name))
	for _, c := range snap.Cards.Vision {
		if strings.ToLower(c.Name) == q || strings.ToLower(c.ID) == q || (c.Numeral != "" && strings.ToLower(c.Numeral) == q) ||
			strings.TrimPrefix(strings.ToLower(c.Name), "the ") == strings.TrimPrefix(q, "the ") {
			return c, nil
		}
	}
	return gamedata.VisionCard{}, fmt.Errorf("no vision card %q", name)
}
