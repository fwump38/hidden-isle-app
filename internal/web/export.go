package web

import (
	"fmt"
	"sort"
	"strings"

	"github.com/fwump38/hidden-isle-app/internal/db"
	"github.com/fwump38/hidden-isle-app/internal/gamedata"
)

// renderMarkdown writes a readable campaign document from an export.
func renderMarkdown(x *export, snap *gamedata.Snapshot) string {
	var b strings.Builder
	w := func(format string, a ...any) { fmt.Fprintf(&b, format, a...) }
	c := x.Campaign
	w("# %s\n\n", c.Name)
	w("**Mode:** %s · **Merciful Mode:** %s · **Season:** %d · **Scenarios played:** %d\n\n", c.Mode, onOff(c.Merciful), c.Season, c.ScenariosPlayed)
	if c.HandName != "" || c.HandMascot != "" {
		w("**The Hand:** %s (mascot: %s)\n\n", c.HandName, c.HandMascot)
	}
	section := func(title, body string) {
		if strings.TrimSpace(body) != "" {
			w("## %s\n%s\n\n", title, strings.TrimSpace(body))
		}
	}
	section("Table", c.Table)
	section("Options in use", c.Options)
	section("Open threads", c.OpenThreads)

	w("## Agents\n\n")
	for _, a := range x.Agents {
		class := a.Class
		if snap != nil && snap.Class(a.Class) != nil {
			class = snap.Class(a.Class).Name
		}
		w("### %s (%s, %s)\n", a.Name, class, a.Status)
		w("- **Burden:** %s %s, track %d/7 · **Ideal:** %s %s, track %d/7\n", a.Burden, paren(a.BurdenCard), a.BurdenTrack, a.Ideal, paren(a.IdealCard), a.IdealTrack)
		if len(a.Vices)+len(a.Virtues) > 0 {
			w("- **Vices:** %s · **Virtues:** %s\n", strings.Join(a.Vices, ", "), strings.Join(a.Virtues, ", "))
		}
		var skills []string
		for sk, n := range a.Skills {
			if n > 0 {
				skills = append(skills, fmt.Sprintf("%s %d", sk, n))
			}
		}
		sort.Strings(skills)
		w("- **Skills:** %s\n", strings.Join(skills, ", "))
		var harm []string
		for suit, marks := range a.Harm {
			if len(marks) > 0 {
				harm = append(harm, suit+" "+strings.Join(marks, " "))
			}
		}
		sort.Strings(harm)
		if len(harm) > 0 {
			w("- **Harm:** %s\n", strings.Join(harm, "; "))
		}
		w("- **XP:** Swords %d, Wands %d, Cups %d, Pentacles %d, abilities %d\n", a.XPSwords, a.XPWands, a.XPCups, a.XPPentacles, a.XPAbility)
		for _, ab := range a.Abilities {
			name, text := ab.Name, ab.Text
			if ab.ID != "" && snap != nil {
				name, text = abilityLookup(snap, ab.ID)
			}
			w("- **%s:** %s\n", name, text)
		}
		for _, ct := range x.Contacts {
			if ct.AgentID == a.ID {
				w("- Contact **%s** (%s, %s): affection %d, distance %d. %s\n", ct.Name, ct.Kind, ct.Card, ct.Affection, ct.Distance, ct.Description)
			}
		}
		w("\n")
	}
	if len(x.Adversaries) > 0 {
		w("## Adversaries\n\n")
		for _, ad := range x.Adversaries {
			w("- **%s** (%s): progress %d/%d. %s\n", ad.Name, ad.Status, ad.Progress, ad.TrackLength, ad.Plot)
			if ad.Secrets != "" {
				w("  - SEER ONLY: %s\n", ad.Secrets)
			}
		}
		w("\n")
	}
	if len(x.Clocks) > 0 {
		w("## Clocks\n\n")
		for _, cl := range x.Clocks {
			seer := ""
			if cl.Visibility == db.VisSeer {
				seer = " (SEER ONLY)"
			}
			w("- **%s**: %d/%d, %s%s\n", cl.Name, cl.Filled, cl.Segments, cl.Status, seer)
		}
		w("\n")
	}
	if len(x.Territories) > 0 {
		w("## Territories\n\n")
		for _, t := range x.Territories {
			w("- **%s:** %s\n", t.Name, strings.TrimSpace(t.Events+" "+t.Notes))
		}
		w("\n")
	}
	if len(x.Sessions) > 0 {
		w("## Sessions\n\n")
		for _, s := range x.Sessions {
			w("### %d. %s (%s)\n%s\n\n", s.Number, s.Title, s.Status, s.Summary)
			if s.Prep != "" {
				w("SEER ONLY, prep: %s\n\n", s.Prep)
			}
		}
	}
	if len(x.HouseRulings) > 0 {
		w("## House rulings\n\n")
		for _, h := range x.HouseRulings {
			w("- %s: %s %s\n", h.CreatedAt.Format("2006-01-02"), h.Ruling, paren(h.Page))
		}
		w("\n")
	}
	if len(x.Entries) > 0 {
		w("## Writing\n\n")
		for _, e := range x.Entries {
			w("### %s (%s, %s)\n%s\n\n", e.Title, e.Kind, e.CreatedAt.Format("2006-01-02"), plainMentions(e.Body))
		}
	}
	if len(x.SeerNotes) > 0 {
		w("## Seer notes (SEER ONLY)\n\n")
		for _, n := range x.SeerNotes {
			w("### %s\n%s\n\n", n.Title, plainMentions(n.Body))
		}
	}
	return b.String()
}

func abilityLookup(snap *gamedata.Snapshot, id string) (string, string) {
	for _, c := range snap.Classes.Classes {
		for _, a := range c.Abilities {
			if a.ID == id {
				return a.Name, fmt.Sprintf("%s (p. %d)", a.Text, a.Page)
			}
		}
	}
	return id, ""
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

func paren(s string) string {
	if s == "" {
		return ""
	}
	return "(" + s + ")"
}
