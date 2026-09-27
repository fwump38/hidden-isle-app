package web

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/fwump38/hidden-isle-app/internal/campaign"
	"github.com/fwump38/hidden-isle-app/internal/db"
)

func (s *Server) registerDowntime(mux *http.ServeMux) {
	u := func(h http.HandlerFunc) http.Handler { return s.requireUser(h) }
	mux.Handle("GET /agents/{id}/downtime", u(s.downtimePage))
	mux.Handle("POST /agents/{id}/downtime", u(s.submitDowntime))
	mux.Handle("GET /c/{cid}/downtime", u(s.downtimeQueue))
	mux.Handle("POST /downtime/{id}/approve", u(s.approveDowntime))
	mux.Handle("POST /downtime/{id}/reject", u(s.rejectDowntime))
}

const downtimeActionSlots = 3 // 2 free + 1 extra (or, solo, 3 free + 1 extra — the extra slot still just needs the checkbox)

type downtimePageData struct {
	Agent     *db.Agent
	FreeSlots int
	Vices     []string
	Contacts  []db.Contact
	Mine      []db.DowntimeSubmission
	Error     string
}

func (s *Server) downtimePage(w http.ResponseWriter, r *http.Request) {
	a := s.actor(r)
	ag, err := s.Svc.Agent(a, pathID(r, "id"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if !s.Svc.CanEditAgent(a, ag) {
		s.fail(w, r, campaign.ErrForbidden)
		return
	}
	if ag.CampaignID == 0 {
		s.fail(w, r, fmt.Errorf("bring %s into a campaign before running downtime", ag.Name))
		return
	}
	d := s.buildDowntimePage(a, ag)
	var c *db.Campaign
	if ag.CampaignID != 0 {
		c, _ = s.Svc.Campaign(a, ag.CampaignID)
	}
	s.render(w, r, "downtime", http.StatusOK, pageData{Title: "Downtime: " + ag.Name, Error: takeFlash(w, r), Data: d,
		Nav: &campaignNav{Campaign: c, Section: "agents", Snap: s.Data.Current()}})
}

func (s *Server) buildDowntimePage(a campaign.Actor, ag *db.Agent) downtimePageData {
	free := 2
	if snap := s.Data.Current(); snap != nil {
		free = snap.Limits.Downtime.FreeActions
		if c, err := s.Svc.Campaign(a, ag.CampaignID); err == nil && c.Mode == "solitaire" && snap.Limits.Downtime.SolitaireFreeActions > 0 {
			free = snap.Limits.Downtime.SolitaireFreeActions
		}
	}
	contacts, _ := s.Svc.Contacts(a, ag.ID)
	mine, _ := s.Svc.DowntimeSubmissions(a, ag.CampaignID, "")
	var own []db.DowntimeSubmission
	for _, m := range mine {
		if m.AgentID == ag.ID {
			own = append(own, m)
		}
	}
	return downtimePageData{Agent: ag, FreeSlots: free, Vices: ag.Vices, Contacts: contacts, Mine: own}
}

// buildDowntimeSubmission reads the downtime form into a submission, pairing each vice by
// position with the Agent's own vice list (never trusting a vice name from the form).
func buildDowntimeSubmission(ag *db.Agent, f map[string][]string) *db.DowntimeSubmission {
	get := func(k string) string {
		if v := f[k]; len(v) > 0 {
			return v[len(v)-1]
		}
		return ""
	}
	sub := &db.DowntimeSubmission{PlayerNote: strings.TrimSpace(get("player_note")), ExtraAction: get("extra_action") == "on",
		ExtraActionSuit: get("extra_action_suit")}
	for i, vice := range ag.Vices {
		sub.Vices = append(sub.Vices, db.DowntimeVice{Vice: vice, Suit: get(fmt.Sprintf("vice%d.suit", i))})
	}
	for i := 0; i < downtimeActionSlots; i++ {
		p := fmt.Sprintf("action%d.", i)
		kind := get(p + "kind")
		if kind == "" {
			continue
		}
		amount, _ := strconv.Atoi(get(p + "amount"))
		delta, _ := strconv.Atoi(get(p + "delta"))
		contactID, _ := strconv.ParseUint(get(p+"contact_id"), 10, 64)
		clockID, _ := strconv.ParseUint(get(p+"clock_id"), 10, 64)
		segments, _ := strconv.Atoi(get(p + "segments"))
		track := strings.TrimSpace(get(p + "track"))
		if school := strings.TrimSpace(get(p + "proficiency")); school != "" {
			track = "proficiency:" + school
		}
		sub.Actions = append(sub.Actions, db.DowntimeAction{
			Kind: kind, HarmType: get(p + "harm_type"), Amount: amount, Suit: get(p + "suit"), Track: track,
			ClockID: uint(clockID), Segments: segments, Trait: get(p + "trait"), TrackDelta: delta,
			District: strings.TrimSpace(get(p + "district")), ContactName: strings.TrimSpace(get(p + "contact_name")),
			ContactCard: strings.TrimSpace(get(p + "contact_card")), ContactDescription: strings.TrimSpace(get(p + "contact_description")),
			ContactID: uint(contactID), Activity: get(p + "activity"), Note: strings.TrimSpace(get(p + "note")),
		})
	}
	return sub
}

func (s *Server) submitDowntime(w http.ResponseWriter, r *http.Request) {
	a := s.actor(r)
	id := pathID(r, "id")
	back := fmt.Sprintf("/agents/%d/downtime", id)
	ag, err := s.Svc.Agent(a, id)
	if err != nil {
		s.done(w, r, err, back)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.done(w, r, err, back)
		return
	}
	sub := buildDowntimeSubmission(ag, r.PostForm)
	if _, err := s.Svc.SubmitDowntime(a, id, sub); err != nil {
		s.done(w, r, err, back)
		return
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// ---------------------------------------------------------------- Seer queue

type downtimeQueueData struct {
	Pending []queuedSubmission
	Done    []queuedSubmission
}

type queuedSubmission struct {
	db.DowntimeSubmission
	AgentName string
}

func (s *Server) downtimeQueue(w http.ResponseWriter, r *http.Request) {
	a := s.actor(r)
	if !a.IsSeer() {
		s.fail(w, r, campaign.ErrForbidden)
		return
	}
	c, err := s.Svc.Campaign(a, pathID(r, "cid"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	all, err := s.Svc.DowntimeSubmissions(a, c.ID, "")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	names := map[uint]string{}
	agents, _ := s.Svc.Agents(a, c.ID)
	for _, ag := range agents {
		names[ag.ID] = ag.Name
	}
	var d downtimeQueueData
	for _, sub := range all {
		q := queuedSubmission{DowntimeSubmission: sub, AgentName: names[sub.AgentID]}
		if sub.Status == campaign.DowntimeStatusPending {
			d.Pending = append(d.Pending, q)
		} else {
			d.Done = append(d.Done, q)
		}
	}
	s.page(w, r, "downtime-queue", "Downtime", c, "downtime", d)
}

func (s *Server) approveDowntime(w http.ResponseWriter, r *http.Request) {
	id := pathID(r, "id")
	err := s.Svc.ApproveDowntime(s.actor(r), id, strings.TrimSpace(r.FormValue("note")))
	s.done(w, r, err, "/")
}

func (s *Server) rejectDowntime(w http.ResponseWriter, r *http.Request) {
	id := pathID(r, "id")
	err := s.Svc.RejectDowntime(s.actor(r), id, strings.TrimSpace(r.FormValue("reason")))
	s.done(w, r, err, "/")
}
