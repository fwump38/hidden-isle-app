package web

import (
	"net/http"
	"strconv"

	"github.com/fwump38/hidden-isle-app/internal/assist"
	"github.com/fwump38/hidden-isle-app/internal/db"
)

// The writing assistant: one endpoint behind every "Enhance" or "Draft from campaign" button in
// the app. It never saves anything — the result is a preview the caller Replaces, Appends into,
// or Discards from their own form, exactly like the wizard's suggest boxes.

func (s *Server) registerAssist(mux *http.ServeMux) {
	mux.Handle("POST /assist/write", s.requireUser(http.HandlerFunc(s.assistWrite)))
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
