package web

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/fwump38/hidden-isle-app/internal/cards"
	"github.com/fwump38/hidden-isle-app/internal/challenge"
	"github.com/fwump38/hidden-isle-app/internal/db"
)

// challengeView is what the challenge helper shows after a count, draw or resolve.
type challengeView struct {
	AgentID uint
	CanEdit bool
	Count   *challenge.Count
	Result  *challenge.Result
	Drawn   []string
	Error   string
	Ideas   []string
	Form    url.Values
}

// Consequence menus (p. 17 failure, p. 18 complicated success).
var failureIdeas = []string{"harm (1-3, physical or spiritual)", "lose an item, for now or for good", "a complication", "rising danger: start or tick a clock",
	"a future disadvantage", "the enemy acts", "a lost opportunity", "higher stakes", "interpersonal conflict"}
var complicatedIdeas = []string{"harm", "a cost: a favor, a price, a hard choice", "lose an item", "reduced effect", "rising danger: start or tick a clock",
	"a lost opportunity", "an unexpected problem", "higher stakes", "stuck? draw a vision card for inspiration"}

func setupFromForm(ag *db.Agent, f url.Values) challenge.Setup {
	skill := f.Get("skill")
	st := challenge.Setup{Skill: skill, SkillPoints: ag.Skills[skill], HarmInSuit: len(ag.Harm[challenge.SuitOf(skill)]),
		IgnoreHarm: f.Get("ignore_harm") == "on", Burden: f.Get("burden") == "on", Vice: f.Get("vice") == "on",
		Ideal: f.Get("ideal") == "on", Virtue: f.Get("virtue") == "on", Difficulty: orDefault(f.Get("difficulty"), "medium")}
	st.SeerExtra, _ = strconv.Atoi(f.Get("seer_extra"))
	st.Participants, _ = strconv.Atoi(f.Get("participants"))
	for i := 0; i < 3; i++ {
		n, _ := strconv.Atoi(f.Get(fmt.Sprintf("mod%d", i)))
		if n != 0 {
			st.Modifiers = append(st.Modifiers, challenge.Modifier{Label: orDefault(f.Get(fmt.Sprintf("mod%d_label", i)), "modifier"), Cards: n})
		}
	}
	return st
}

// challengeHelper handles the sheet's challenge form: count, draw (digital, on request) or resolve.
func (s *Server) challengeHelper(w http.ResponseWriter, r *http.Request) {
	a := s.actor(r)
	ag, err := s.Svc.Agent(a, pathID(r, "id"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	_ = r.ParseForm()
	f := r.PostForm
	v := challengeView{AgentID: ag.ID, CanEdit: s.Svc.CanEditAgent(a, ag), Form: f}
	defer func() { s.partial(w, "agent", "challenge-out", v) }()

	count, err := challenge.Counts(setupFromForm(ag, f))
	if err != nil {
		v.Error = err.Error()
		return
	}
	v.Count = &count
	switch f.Get("action") {
	case "draw":
		snap := s.Data.Current()
		if snap == nil {
			v.Error = "rules data isn't loaded"
			return
		}
		hands, err := cards.Draw(snap, "pips", []cards.Request{{Count: count.AgentCards}})
		if err != nil {
			v.Error = err.Error()
			return
		}
		v.Drawn = hands[0].Cards
	case "resolve":
		played, err1 := challenge.ParseCard(f.Get("played"))
		seer, err2 := challenge.ParseCard(f.Get("seer"))
		if err1 != nil || err2 != nil {
			v.Error = "Choose the card you played and the Seer's card."
			return
		}
		var fortunes []challenge.Fortune
		for i := 0; i < 3; i++ {
			fc := f.Get(fmt.Sprintf("fortune%d", i))
			if fc == "" {
				continue
			}
			c, err := challenge.ParseCard(fc)
			if err != nil {
				v.Error = err.Error()
				return
			}
			fortunes = append(fortunes, challenge.Fortune{Card: c, Mode: orDefault(f.Get(fmt.Sprintf("fortune%d_mode", i)), "add"),
				SuitBonus: f.Get(fmt.Sprintf("fortune%d_bonus", i)) == "on", By: strings.TrimSpace(f.Get(fmt.Sprintf("fortune%d_by", i)))})
		}
		res, err := challenge.Resolve(count.Trump, played, seer, count.NumeralBonus, fortunes)
		if err != nil {
			v.Error = err.Error()
			return
		}
		v.Result = &res
		switch res.Outcome {
		case "failure":
			v.Ideas = failureIdeas
		case "complicated":
			v.Ideas = complicatedIdeas
		}
	}
}

// pipNames lists the 40 pips for card pickers.
func (s *Server) pipNames() []string {
	var out []string
	if snap := s.Data.Current(); snap != nil {
		for _, c := range snap.Cards.Pips {
			out = append(out, c.Name)
		}
	}
	return out
}
