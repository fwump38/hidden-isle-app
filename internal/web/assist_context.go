package web

import (
	"strings"

	"github.com/fwump38/hidden-isle-app/internal/assist"
	"github.com/fwump38/hidden-isle-app/internal/campaign"
	"github.com/fwump38/hidden-isle-app/internal/db"
)

// briefOpts controls how much of a campaign buildBrief pulls in.
type briefOpts struct {
	AgentID   uint // include this Agent's own details, contacts and recent history
	SessionID uint // include this session's change log and linked entries (for drafting a recap)
	// Public means the text being written may end up in front of players: leave out everything
	// Seer-only (secrets, hidden adversaries, session prep, Seer notes, Seer-visibility clocks)
	// even when the caller is the Seer, so nothing secret can leak into player-facing prose.
	Public bool
}

// buildBrief assembles campaign context for a writing-assistant or suggestion request, through
// campaign.Service with the caller's Actor so the usual permission checks decide what a non-Seer
// caller can see at all. For the Seer, opts.Public additionally strips content that's fine for
// the Seer to read but shouldn't end up drafted into something players will read.
func buildBrief(a campaign.Actor, svc *campaign.Service, campaignID uint, opts briefOpts) *assist.Brief {
	b := &assist.Brief{}
	if campaignID == 0 {
		// Not yet in a campaign (e.g. the creation wizard before the Agent joins one): there's no
		// campaign-wide context to add, but the Agent's own details still apply.
		if opts.AgentID != 0 {
			addAgentDetail(a, svc, b, opts.AgentID, opts.Public)
		}
		return b
	}

	if c, err := svc.Campaign(a, campaignID); err == nil {
		b.Add("Campaign: %s (season %d, mode %s)", c.Name, c.Season, c.Mode)
		if c.HandName != "" || c.HandMascot != "" {
			b.Add("The party is the Hand: %s %s", c.HandName, c.HandMascot)
		}
		if c.Table != "" {
			b.Add("Table agreements (tone, lines and veils): %s", c.Table)
		}
		if opts := campaignOptions(c); len(opts) > 0 || c.Options != "" {
			line := strings.Join(opts, ", ")
			if c.Options != "" {
				if line != "" {
					line += "; "
				}
				line += c.Options
			}
			b.Add("House options in use: %s", line)
		}
		if c.OpenThreads != "" {
			b.Add("Open threads: %s", c.OpenThreads)
		}
	}

	if agents, err := svc.Agents(a, campaignID); err == nil {
		for _, ag := range agents {
			b.Add("Agent: %s, a %s. Burden: %s. Ideal: %s.", ag.Name, ag.Class, ag.Burden, ag.Ideal)
		}
	}

	if opts.AgentID != 0 {
		addAgentDetail(a, svc, b, opts.AgentID, opts.Public)
	}

	var advs []db.Adversary
	if err := svc.List(a, "adversary", campaignID, &advs, ""); err == nil {
		for _, adv := range advs {
			if opts.Public && adv.Hidden {
				continue
			}
			b.Add("Adversary: %s. Plot: %s. Motivation: %s.", adv.Name, adv.Plot, adv.Motivation)
			if !opts.Public && adv.Secrets != "" {
				b.Add("Adversary %s's secrets (Seer-only): %s", adv.Name, adv.Secrets)
			}
		}
	}

	var terrs []db.Territory
	if err := svc.List(a, "territory", campaignID, &terrs, ""); err == nil {
		for _, t := range terrs {
			b.Add("Territory %s — events: %s. Contacts: %s.", t.Name, t.Events, t.Contacts)
		}
	}

	var clocks []db.Clock
	if err := svc.List(a, "clock", campaignID, &clocks, ""); err == nil {
		for _, c := range clocks {
			if opts.Public && c.Visibility == db.VisSeer {
				continue
			}
			b.Add("Clock: %s (%d/%d, %s)", c.Name, c.Filled, c.Segments, c.Status)
		}
	}

	var sessions []db.Session
	if err := svc.List(a, "session", campaignID, &sessions, "date desc"); err == nil {
		for i, s := range sessions {
			if i >= 3 && s.ID != opts.SessionID {
				continue
			}
			if i >= 6 {
				break
			}
			b.Add("Session %q (%s): %s", s.Title, s.Status, s.Summary)
			if !opts.Public {
				if s.Prep != "" {
					b.Add("Session %q prep (Seer-only): %s", s.Title, s.Prep)
				}
				if s.Divination != "" {
					b.Add("Session %q divination (Seer-only): %s", s.Title, s.Divination)
				}
			}
		}
	}

	if entries, err := svc.Entries(a, campaignID, "recap", 0); err == nil {
		for _, e := range entries {
			if !e.Published {
				continue
			}
			b.Add("Published recap %q: %s", e.Title, e.Body)
		}
	}

	if opts.SessionID != 0 {
		addSessionChangeLog(a, svc, b, campaignID, opts.SessionID)
	}

	return b
}

// addAgentDetail adds one Agent's own concept, contacts and recent history to the brief.
func addAgentDetail(a campaign.Actor, svc *campaign.Service, b *assist.Brief, agentID uint, public bool) {
	ag, err := svc.Agent(a, agentID)
	if err != nil {
		return
	}
	b.Add("This Agent, %s: class %s, why they came to Dioscoria: %s. Concept: %s.", ag.Name, ag.Class, ag.Why, ag.Concept)
	if contacts, err := svc.Contacts(a, agentID); err == nil {
		for _, c := range contacts {
			b.Add("%s's contact %s (%s): %s", ag.Name, c.Name, c.Kind, c.Description)
		}
	}
	if hist, err := svc.AgentHistory(a, agentID); err == nil {
		for i, e := range hist {
			if i >= 5 {
				break
			}
			if public && e.Visibility != db.VisParty {
				continue
			}
			b.Add("%s's history — %s: %s", ag.Name, e.Title, e.Body)
		}
	}
}

// addSessionChangeLog adds a session's own change log summaries, for drafting a recap or "next
// time" notes from what actually happened, never invented.
func addSessionChangeLog(a campaign.Actor, svc *campaign.Service, b *assist.Brief, campaignID, sessionID uint) {
	events, err := svc.Events(a, campaignID, campaign.EventFilter{SessionID: sessionID, Limit: 40})
	if err != nil {
		return
	}
	for _, e := range events {
		if e.Summary == "" {
			continue
		}
		b.Add("Logged during this session — %s: %s", e.EntityName, e.Summary)
	}
}
