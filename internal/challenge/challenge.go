// Package challenge counts challenge hands and resolves played cards, following Rulebook 1.4
// pp. 15-22. It only does arithmetic and comparisons: which cards are drawn or played is always
// the table's (real cards) or an explicit digital draw.
package challenge

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

var Suits = []string{"Swords", "Wands", "Cups", "Pentacles"}

// Card is a pips card. Rank 1 is the Ace (worth 11 in challenges, p. 16).
type Card struct {
	Suit string `json:"suit"`
	Rank int    `json:"rank"`
}

func (c Card) Value() int {
	if c.Rank == 1 {
		return 11
	}
	return c.Rank
}

func (c Card) String() string {
	r := strconv.Itoa(c.Rank)
	if c.Rank == 1 {
		r = "Ace"
	}
	return r + " of " + c.Suit
}

// ParseCard reads "Ace of Swords", "7 of cups", "10 of Pentacles".
func ParseCard(s string) (Card, error) {
	rank, suit, ok := strings.Cut(strings.TrimSpace(s), " of ")
	if !ok {
		return Card{}, fmt.Errorf("%q isn't a pips card (e.g. \"7 of Cups\", \"Ace of Swords\")", s)
	}
	var c Card
	for _, x := range Suits {
		if strings.EqualFold(x, strings.TrimSpace(suit)) {
			c.Suit = x
		}
	}
	switch r := strings.ToLower(strings.TrimSpace(rank)); r {
	case "ace", "a", "1":
		c.Rank = 1
	default:
		n, err := strconv.Atoi(r)
		if err != nil || n < 2 || n > 10 {
			return Card{}, fmt.Errorf("%q: the pips deck runs Ace to 10", s)
		}
		c.Rank = n
	}
	if c.Suit == "" {
		return Card{}, fmt.Errorf("%q: suit must be Swords, Wands, Cups or Pentacles", s)
	}
	return c, nil
}

// ---------------------------------------------------------------- counting

// Modifier is an extra card (+1) or a lost one (-1) with its reason: preparation, a vision, an
// item, an ally, an ability (p. 15).
type Modifier struct {
	Label string `json:"label"`
	Cards int    `json:"cards"`
}

type Setup struct {
	Skill       string     `json:"skill"`
	SkillPoints int        `json:"skill_points"`
	HarmInSuit  int        `json:"harm_in_suit"`
	IgnoreHarm  bool       `json:"ignore_harm,omitempty"` // e.g. UNSTOPPABLE
	Burden      bool       `json:"burden,omitempty"`
	Vice        bool       `json:"vice,omitempty"`
	Ideal       bool       `json:"ideal,omitempty"`
	Virtue      bool       `json:"virtue,omitempty"`
	Modifiers   []Modifier `json:"modifiers,omitempty"`

	Difficulty   string `json:"difficulty"`             // easy, medium, hard
	SeerExtra    int    `json:"seer_extra,omitempty"`   // dangerous context (p. 15)
	Participants int    `json:"participants,omitempty"` // group action: +1 Seer card each (p. 22)
}

type Count struct {
	Trump         string   `json:"trump"`
	AgentCards    int      `json:"agent_cards"`
	NumeralBonus  int      `json:"numeral_bonus"` // added to every card's number (ideal/virtue)
	SeerCards     int      `json:"seer_cards"`
	MarkBurden    bool     `json:"mark_burden,omitempty"`
	MarkIdeal     bool     `json:"mark_ideal,omitempty"`
	Breakdown     []string `json:"breakdown"`
	SeerBreakdown []string `json:"seer_breakdown"`
	Notes         []string `json:"notes,omitempty"`
}

var suitOfSkill = map[string]string{
	"skirmish": "Swords", "convince": "Swords", "study": "Swords",
	"unleash": "Wands", "perform": "Wands", "channel": "Wands",
	"slip": "Cups", "soothe": "Cups", "mingle": "Cups",
	"finesse": "Pentacles", "bargain": "Pentacles", "survey": "Pentacles",
}

// SuitOf returns a skill's suit ("" if unknown).
func SuitOf(skill string) string { return suitOfSkill[strings.ToLower(strings.TrimSpace(skill))] }

// Counts works out both hands (p. 15, p. 20, p. 22, p. 23).
func Counts(s Setup) (Count, error) {
	trump := SuitOf(s.Skill)
	if trump == "" {
		return Count{}, fmt.Errorf("unknown skill %q", s.Skill)
	}
	c := Count{Trump: trump}
	n := 1 + s.SkillPoints
	c.Breakdown = append(c.Breakdown, fmt.Sprintf("1 + %d %s = %d (p. 15)", s.SkillPoints, s.Skill, n))
	if s.Burden {
		n++
		c.MarkBurden = true
		c.Breakdown = append(c.Breakdown, "+1 burden; mark the burden track (p. 20)")
	}
	if s.Vice {
		n++
		c.Breakdown = append(c.Breakdown, "+1 vice (p. 20)")
	}
	if s.Ideal {
		n--
		c.NumeralBonus += 3
		c.MarkIdeal = true
		c.Breakdown = append(c.Breakdown, "-1 ideal, +3 to every card's number; mark the ideal track (p. 20)")
	}
	if s.Virtue {
		c.NumeralBonus += 3
		c.Breakdown = append(c.Breakdown, "virtue: +3 to every card's number (p. 20)")
	}
	if s.Ideal && s.Virtue {
		c.Notes = append(c.Notes, "Ideal and virtue together: the book doesn't say whether the two +3s stack; this counts +6. The Seer rules.")
	}
	if s.HarmInSuit >= 2 {
		if s.IgnoreHarm {
			c.Breakdown = append(c.Breakdown, "2 harm in "+trump+", ignored (ability)")
		} else {
			n--
			c.Breakdown = append(c.Breakdown, "-1 for 2 harm in "+trump+" (p. 23)")
		}
	}
	for _, m := range s.Modifiers {
		if m.Cards == 0 {
			continue
		}
		n += m.Cards
		c.Breakdown = append(c.Breakdown, fmt.Sprintf("%+d %s", m.Cards, m.Label))
	}
	if n < 1 {
		c.Breakdown = append(c.Breakdown, fmt.Sprintf("minimum 1 card (was %d) (p. 15)", n))
		n = 1
	}
	c.AgentCards = n

	base, ok := map[string]int{"easy": 2, "medium": 3, "hard": 4}[strings.ToLower(s.Difficulty)]
	if !ok {
		return Count{}, errors.New("difficulty must be easy, medium or hard")
	}
	c.SeerCards = base
	c.SeerBreakdown = append(c.SeerBreakdown, fmt.Sprintf("%d %s (p. 15)", base, strings.ToLower(s.Difficulty)))
	if s.SeerExtra != 0 {
		c.SeerCards += s.SeerExtra
		c.SeerBreakdown = append(c.SeerBreakdown, fmt.Sprintf("%+d dangerous context (p. 15)", s.SeerExtra))
	}
	if s.Participants > 0 {
		c.SeerCards += s.Participants
		c.SeerBreakdown = append(c.SeerBreakdown, fmt.Sprintf("+%d group action, one per participant (p. 22)", s.Participants))
	}
	if c.SeerCards < 1 {
		c.SeerCards = 1
	}
	return c, nil
}

// ---------------------------------------------------------------- resolving

// Fortune is a fortune card played after the reveal (pp. 18-19): it either changes the challenge
// card's suit to its own, or adds its number. SuitBonus: the player who plays it has at least 1
// point in all three skills of the fortune card's suit, for +3 more.
type Fortune struct {
	Card      Card   `json:"card"`
	Mode      string `json:"mode"` // suit | add
	SuitBonus bool   `json:"suit_bonus,omitempty"`
	By        string `json:"by,omitempty"`
}

type Result struct {
	Outcome    string   `json:"outcome"` // total | complicated | failure
	Label      string   `json:"label"`
	Challenge  string   `json:"challenge_card"` // effective, after bonuses and fortune
	ChallengeN int      `json:"challenge_number"`
	Seer       string   `json:"seer_card"`
	SeerN      int      `json:"seer_number"`
	Steps      []string `json:"steps"`
	Notes      []string `json:"notes,omitempty"`
}

var labels = map[string]string{"total": "Total success", "complicated": "Complicated success", "failure": "Failure"}

// Resolve compares the challenger's and the Seer's cards (pp. 15-16), after the numeral bonus
// (ideal/virtue) and any fortune cards (pp. 18-19).
func Resolve(trump string, played, seer Card, numeralBonus int, fortunes []Fortune) (Result, error) {
	if trump == "" {
		return Result{}, errors.New("no trump suit")
	}
	if len(fortunes) > 0 && numeralBonus < 0 {
		return Result{}, errors.New("bad numeral bonus")
	}
	suit, n := played.Suit, played.Value()
	r := Result{Seer: seer.String(), SeerN: seer.Value()}
	r.Steps = append(r.Steps, fmt.Sprintf("Trump is %s. Challenge card %s (%d), Seer %s (%d). Ace = 11 (p. 16).", trump, played, played.Value(), seer, seer.Value()))
	if numeralBonus != 0 {
		n += numeralBonus
		r.Steps = append(r.Steps, fmt.Sprintf("+%d to the number (ideal/virtue, p. 20): %d", numeralBonus, n))
	}
	suitChanged := false
	for _, f := range fortunes {
		who := ""
		if f.By != "" {
			who = f.By + "'s "
		}
		switch f.Mode {
		case "suit":
			suit = f.Card.Suit
			suitChanged = true
			r.Steps = append(r.Steps, fmt.Sprintf("%sfortune %s changes the suit to %s (p. 18)", who, f.Card, suit))
		case "add":
			n += f.Card.Value()
			r.Steps = append(r.Steps, fmt.Sprintf("%sfortune %s adds %d: %d (p. 18)", who, f.Card, f.Card.Value(), n))
		default:
			return Result{}, fmt.Errorf("fortune mode must be suit or add")
		}
		if f.SuitBonus {
			n += 3
			r.Steps = append(r.Steps, fmt.Sprintf("+3 suit bonus (points in all three %s skills, p. 19): %d", f.Card.Suit, n))
		}
	}
	if len(fortunes) > 3 {
		r.Notes = append(r.Notes, "One fortune card per player per challenge (p. 18).")
	}
	r.Challenge = fmt.Sprintf("%s of %s", rankName(played.Rank), suit)
	r.ChallengeN = n
	agentTrump, seerTrump := suit == trump, seer.Suit == trump

	switch {
	case played.Rank == 1 && agentTrump:
		r.Outcome = "total"
		r.Steps = append(r.Steps, "A trump Ace played by the challenger is always a total success (p. 16).")
		if suitChanged {
			r.Notes = append(r.Notes, "This Ace became trump through a fortune card. The book's trump-Ace rule speaks of the card played; whether it also covers a suit changed by fortune is the Seer's call.")
		}
	case agentTrump && !seerTrump:
		r.Outcome = "total"
		r.Steps = append(r.Steps, "Only the challenger's card is trump: the trump wins, a total success (p. 16).")
	case seerTrump && !agentTrump:
		r.Outcome = "failure"
		r.Steps = append(r.Steps, "Only the Seer's card is trump: the trump wins, a failure (p. 16).")
	default:
		both := "Neither card is trump"
		if agentTrump {
			both = "Both cards are trump"
		}
		if n >= seer.Value() {
			r.Outcome = "complicated"
			tie := ""
			if n == seer.Value() {
				tie = " (ties go to the challenger)"
			}
			r.Steps = append(r.Steps, fmt.Sprintf("%s: %d vs %d, the challenger wins%s, a complicated success (p. 16).", both, n, seer.Value(), tie))
		} else {
			r.Outcome = "failure"
			r.Steps = append(r.Steps, fmt.Sprintf("%s: %d vs %d, the higher number wins, a failure (p. 16).", both, n, seer.Value()))
		}
	}
	r.Label = labels[r.Outcome]
	return r, nil
}

func rankName(r int) string {
	if r == 1 {
		return "Ace"
	}
	return strconv.Itoa(r)
}
