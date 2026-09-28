package web

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/fwump38/hidden-isle-app/internal/assist"
	"github.com/fwump38/hidden-isle-app/internal/db"
)

// The writing assistant: one endpoint behind every "Enhance" or "Draft from campaign" button in
// the app, plus the Seer's own structured "Suggest" boxes (adversaries, sessions, clocks,
// territory events, handouts) alongside the creation wizard's. Neither ever saves anything — the
// result is a preview or a set of choices the caller applies to their own form, and nothing
// touches the database until they save it themselves.

func (s *Server) registerAssist(mux *http.ServeMux) {
	mux.Handle("POST /assist/write", s.requireUser(http.HandlerFunc(s.assistWrite)))
	mux.Handle("POST /c/{cid}/suggest", s.requireUser(http.HandlerFunc(s.seerSuggest)))
}

// assistPreviewData renders the "assist-preview" partial under the field that asked for it.
type assistPreviewData struct {
	Target string // the textarea's name, so the template can target it without guessing
	Text   string
	Error  string
}

func formID(r *http.Request, name string) uint {
	n, _ := strconv.ParseUint(r.FormValue(name), 10, 64)
	return uint(n)
}

func (s *Server) assistWrite(w http.ResponseWriter, r *http.Request) {
	a := s.actor(r)
	_ = r.ParseForm()
	target := r.FormValue("target")
	af, ok := assistFields[r.FormValue("field")]
	d := assistPreviewData{Target: target}

	switch {
	case s.Assist == nil:
		d.Error = "Writing help needs the in-app assistant, which isn't set up."
	case !ok:
		d.Error = "Unknown field."
	case af.SeerOnly && !a.IsSeer():
		d.Error = "Only the Seer can use this."
	}
	if d.Error != "" {
		s.partial(w, "journal", "assist-preview", d)
		return
	}

	var agentID uint
	var ag *db.Agent
	if af.NeedsAgent {
		agentID = formID(r, "agent_id")
		var err error
		if agentID != 0 {
			ag, err = s.Svc.Agent(a, agentID)
		}
		if ag == nil || err != nil || !s.Svc.CanEditAgent(a, ag) {
			http.Error(w, friendly(orForbidden(err)), http.StatusForbidden)
			return
		}
	}

	campaignID := formID(r, "campaign_id")
	if campaignID == 0 && ag != nil {
		campaignID = ag.CampaignID
	}
	if campaignID != 0 {
		if _, err := s.Svc.Campaign(a, campaignID); err != nil {
			http.Error(w, friendly(orForbidden(err)), http.StatusForbidden)
			return
		}
	}

	mode := assist.ModeEnhance
	if r.FormValue("mode") == "draft" && af.AllowDraft {
		mode = assist.ModeDraft
	}
	brief := buildBrief(a, s.Svc, campaignID, briefOpts{AgentID: agentID, SessionID: formID(r, "session_id"), Public: af.Public})

	out, err := s.Assist.Write(r.Context(), a.User, assist.WriteRequest{
		Field: af.Label, Mode: mode, Text: r.FormValue("text"), Brief: brief, Guide: af.Guide,
	})
	if err != nil {
		d.Error = sentence(friendly(err))
	} else {
		d.Text = out
	}
	s.partial(w, "journal", "assist-preview", d)
}

// seerKinds are the Suggest kinds only the Seer may ask for, each mapped to the fields its
// choices fill in. "contact" isn't here: it's the wizard's own, player-facing box.
var seerKinds = map[string]bool{"adversary": true, "session": true, "clock": true, "territory_event": true, "handout": true}

// seerSuggest is the Seer's own background helper: "describe what you're after" plus Suggest,
// on the campaign's own records forms, exactly like the wizard's for a new Agent.
func (s *Server) seerSuggest(w http.ResponseWriter, r *http.Request) {
	a := s.actor(r)
	_ = r.ParseForm()
	kind := r.FormValue("kind")
	d := wizSuggestions{Exclude: r.Form["exclude"]}
	switch {
	case s.Assist == nil:
		d.Error = "Suggestions need the in-app assistant, which isn't set up."
	case !seerKinds[kind]:
		d.Error = "Unknown kind."
	case !a.IsSeer():
		d.Error = "Only the Seer can use this."
	}
	if d.Error != "" {
		s.partial(w, "journal", "choices", d)
		return
	}

	campaignID := pathID(r, "cid")
	if _, err := s.Svc.Campaign(a, campaignID); err != nil {
		http.Error(w, friendly(orForbidden(err)), http.StatusForbidden)
		return
	}
	req := assist.SuggestRequest{
		Kind: kind, Brief: buildBrief(a, s.Svc, campaignID, briefOpts{}),
		Hint: r.FormValue("hint"), Exclude: d.Exclude,
	}
	if kind == "clock" {
		if snap := s.Data.Current(); snap != nil {
			var segs []string
			for _, n := range snap.Limits.Clock.Segments {
				segs = append(segs, strconv.Itoa(n))
			}
			req.Enum = map[string][]string{"segments": segs}
		}
	}
	sugs, err := s.Assist.Suggest(r.Context(), a.User, req)
	if err != nil {
		d.Error = sentence(friendly(err))
		s.partial(w, "journal", "choices", d)
		return
	}
	for _, sg := range sugs {
		if c, ok := seerChoiceFor(kind, sg); ok {
			d.Choices = append(d.Choices, c)
			d.Exclude = append(d.Exclude, c.Label)
		}
	}
	if len(d.Choices) == 0 {
		d.Error = "Nothing usable came back. Try again, or describe what you're after."
	}
	s.partial(w, "journal", "choices", d)
}

// seerChoiceFor turns one of the Seer's suggestions into a choice button. Fill keys match the
// field names on the form the box sits under — "set.*" for the generic record forms, plain names
// for the handout form (POST /c/{cid}/handouts).
func seerChoiceFor(kind string, sg assist.Suggestion) (wizChoice, bool) {
	f := sg.Fields
	c := wizChoice{Why: sg.Why, Fill: map[string]string{}}
	switch kind {
	case "adversary":
		c.Label = f["name"]
		c.Fill["set.name"], c.Fill["set.leader"], c.Fill["set.plot"], c.Fill["set.motivation"], c.Fill["set.members"] =
			f["name"], f["leader"], f["plot"], f["motivation"], f["members"]
	case "session":
		c.Label = f["title"]
		c.Fill["set.title"], c.Fill["set.prep"] = f["title"], f["prep"]
	case "clock":
		c.Label = f["name"] + " (" + f["segments"] + ")"
		c.Fill["set.name"], c.Fill["set.segments"], c.Fill["set.linked_to"] = f["name"], f["segments"], f["linked_to"]
	case "territory_event":
		c.Label = f["event"]
		c.Fill["set.events"] = f["event"]
	case "handout":
		c.Label = f["title"]
		c.Fill["title"], c.Fill["body"] = f["title"], f["body"]
	default:
		return c, false
	}
	if strings.TrimSpace(c.Label) == "" {
		return c, false
	}
	return c, true
}
