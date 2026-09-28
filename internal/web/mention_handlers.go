package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/fwump38/hidden-isle-app/internal/db"
)

// The @mention picker (app.js [data-mentions]): GET /c/{cid}/mentions?q= lists who and what a
// body of text can @mention in this campaign, filtered to what the caller may see — a player
// never gets a hidden adversary or another player's private Agent back, since every lookup here
// goes through campaign.Service with their own Actor, same as the rest of the app.

func (s *Server) registerMentions(mux *http.ServeMux) {
	mux.Handle("GET /c/{cid}/mentions", s.requireUser(http.HandlerFunc(s.mentionList)))
}

// mentionItem is one candidate: Kind and ID are what app.js embeds in the @[Label](kind:id)
// token it inserts; Label is shown in the dropdown and becomes the token's display name.
type mentionItem struct {
	Kind  string `json:"kind"`
	ID    string `json:"id"`
	Label string `json:"label"`
}

const mentionLimit = 20

func (s *Server) mentionList(w http.ResponseWriter, r *http.Request) {
	a := s.actor(r)
	cid := pathID(r, "cid")
	if _, err := s.Svc.Campaign(a, cid); err != nil {
		http.Error(w, friendly(orForbidden(err)), http.StatusForbidden)
		return
	}
	q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	var out []mentionItem
	add := func(kind, id, label string) {
		if len(out) >= mentionLimit {
			return
		}
		if q == "" || strings.Contains(strings.ToLower(label), q) {
			out = append(out, mentionItem{kind, id, label})
		}
	}

	var seer db.User
	if s.DB.Where("role = ?", db.RoleSeer).First(&seer).Error == nil {
		add("user", strconv.FormatUint(uint64(seer.ID), 10), seer.Name+" (Seer)")
	}
	if members, err := s.Svc.Members(a, cid); err == nil {
		for _, m := range members {
			add("user", strconv.FormatUint(uint64(m.ID), 10), m.Name)
		}
	}
	if agents, err := s.Svc.Agents(a, cid); err == nil {
		for _, ag := range agents {
			add("agent", strconv.FormatUint(uint64(ag.ID), 10), ag.Name)
			if contacts, err := s.Svc.Contacts(a, ag.ID); err == nil {
				for _, c := range contacts {
					add("contact", fmt.Sprintf("%d.%d", ag.ID, c.ID), c.Name+" ("+ag.Name+"'s contact)")
				}
			}
		}
	}
	var advs []db.Adversary
	if s.Svc.List(a, "adversary", cid, &advs, "") == nil {
		for _, adv := range advs {
			add("adversary", strconv.FormatUint(uint64(adv.ID), 10), adv.Name)
		}
	}
	var terrs []db.Territory
	if s.Svc.List(a, "territory", cid, &terrs, "") == nil {
		for _, t := range terrs {
			add("territory", strconv.FormatUint(uint64(t.ID), 10), t.Name)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"items": out})
}
