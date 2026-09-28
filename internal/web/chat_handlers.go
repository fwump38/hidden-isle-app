package web

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/fwump38/hidden-isle-app/internal/campaign"
	"github.com/fwump38/hidden-isle-app/internal/db"
)

// Chat works with or without a campaign: a player can use it for character-creation help before
// joining one (campaign 0), and from inside a campaign for rules questions grounded in that
// campaign's state. /c/{cid}/chat and the bare /chat share every handler below; cid is simply 0
// on the bare routes.
func (s *Server) registerChat(mux *http.ServeMux) {
	u := func(h http.HandlerFunc) http.Handler { return s.requireUser(h) }
	mux.Handle("GET /chat", u(s.chatPage))
	mux.Handle("GET /chat/panel", u(s.chatPanel))
	mux.Handle("GET /chat/with/{uid}", u(s.chatPage))
	mux.Handle("POST /chat/send", u(s.sendChat))
	mux.Handle("POST /chat/memory", u(s.saveChatMemory))
	mux.Handle("GET /c/{cid}/chat", u(s.chatPage))
	mux.Handle("GET /c/{cid}/chat/panel", u(s.chatPanel))
	mux.Handle("GET /c/{cid}/chat/with/{uid}", u(s.chatPage))
	mux.Handle("POST /c/{cid}/chat/send", u(s.sendChat))
	mux.Handle("POST /c/{cid}/chat/memory", u(s.saveChatMemory))
	mux.Handle("POST /chat/suggest/{id}/apply", u(s.applyChatSuggestion))
	mux.Handle("POST /chat/suggest/{id}/dismiss", u(s.dismissChatSuggestion))
}

type chatPageData struct {
	Enabled     bool
	ReadOnly    bool
	ViewingName string
	Base        string // "/chat" or "/c/{id}/chat"; every form action in chat.html is Base + "/…"
	CampaignID  uint   // 0 outside a campaign
	Thread      *db.ChatThread
	Messages    []db.ChatMessage
	Suggestions []db.ChatSuggestion
	Prefill     string // pre-fills the message box (?ask=)
	Error       string
}

// Pending is the subset chat.html's Apply/Dismiss cards render.
func (d chatPageData) Pending() []db.ChatSuggestion {
	var out []db.ChatSuggestion
	for _, s := range d.Suggestions {
		if s.Status == "pending" {
			out = append(out, s)
		}
	}
	return out
}

// chatPanel renders just the chat for the side panel (layout.html's #chat-drawer), which loads it
// with htmx and keeps it open while the player moves around the app.
func (s *Server) chatPanel(w http.ResponseWriter, r *http.Request) {
	a := s.actor(r)
	cid := pathID(r, "cid")
	if cid != 0 {
		if _, err := s.Svc.Campaign(a, cid); err != nil {
			http.Error(w, friendly(err), http.StatusForbidden)
			return
		}
	}
	d := s.buildChatPage(a, cid, r)
	d.Prefill = r.URL.Query().Get("ask")
	s.partial(w, "chat", "chat-body", d)
}

func (s *Server) chatPage(w http.ResponseWriter, r *http.Request) {
	a := s.actor(r)
	cid := pathID(r, "cid")
	var c *db.Campaign
	if cid != 0 {
		var err error
		if c, err = s.Svc.Campaign(a, cid); err != nil {
			s.fail(w, r, err)
			return
		}
	}
	d := s.buildChatPage(a, cid, r)
	d.Prefill = r.URL.Query().Get("ask")
	if c != nil {
		s.page(w, r, "chat", "Chat", c, "chat", d)
		return
	}
	s.render(w, r, "chat", http.StatusOK, pageData{Title: "Chat", Error: takeFlash(w, r), Data: d})
}

// buildChatPage loads the caller's own thread for campaignID (0 = outside any campaign), or
// (Seer only, via the {uid} path value) a read-only view of a player's thread.
func (s *Server) buildChatPage(a campaign.Actor, campaignID uint, r *http.Request) chatPageData {
	d := chatPageData{Enabled: s.Chat != nil, CampaignID: campaignID}
	if campaignID != 0 {
		d.Base = fmt.Sprintf("/c/%d/chat", campaignID)
	} else {
		d.Base = "/chat"
	}
	if !d.Enabled {
		return d
	}
	userID := a.User.ID
	if uidStr := r.PathValue("uid"); uidStr != "" {
		if !a.IsSeer() {
			d.Error = "only the Seer can read another player's chat"
			return d
		}
		uid, _ := strconv.ParseUint(uidStr, 10, 64)
		userID = uint(uid)
		d.ReadOnly = true
		var u db.User
		s.DB.First(&u, userID)
		d.ViewingName = u.Name
	}
	th, err := s.Chat.Thread(userID, campaignID)
	if err != nil {
		d.Error = friendly(err)
		return d
	}
	d.Thread = th
	d.Messages, d.Suggestions, _ = s.Chat.History(th.ID)
	return d
}

func (s *Server) sendChat(w http.ResponseWriter, r *http.Request) {
	a := s.actor(r)
	cid := pathID(r, "cid")
	d := s.buildChatPage(a, cid, r)
	if s.Chat != nil && d.Thread != nil {
		if _, err := s.Chat.Send(r.Context(), a.User, d.Thread.ID, r.FormValue("message"), r.FormValue("page")); err != nil {
			d.Error = friendly(err)
		}
		d.Messages, d.Suggestions, _ = s.Chat.History(d.Thread.ID)
	}
	s.partial(w, "chat", "chat-body", d)
}

func (s *Server) saveChatMemory(w http.ResponseWriter, r *http.Request) {
	a := s.actor(r)
	cid := pathID(r, "cid")
	d := s.buildChatPage(a, cid, r)
	if s.Chat != nil && d.Thread != nil {
		if err := s.Chat.UpdateMemory(a, d.Thread.ID, r.FormValue("memory")); err != nil {
			d.Error = friendly(err)
		}
		d.Thread, _ = s.Chat.Thread(d.Thread.UserID, cid)
		d.Messages, d.Suggestions, _ = s.Chat.History(d.Thread.ID)
	}
	s.partial(w, "chat", "chat-body", d)
}

func (s *Server) applyChatSuggestion(w http.ResponseWriter, r *http.Request) {
	if s.Chat == nil {
		s.done(w, r, fmt.Errorf("chat isn't enabled"), "/")
		return
	}
	err := s.Chat.ApplySuggestion(s.actor(r), pathID(r, "id"))
	if err == nil && r.Header.Get("HX-Request") == "true" {
		// The page beside the panel may be showing the sheet that just changed.
		w.Header().Set("HX-Trigger", "agentChanged")
	}
	s.suggestionDone(w, r, err)
}

func (s *Server) dismissChatSuggestion(w http.ResponseWriter, r *http.Request) {
	if s.Chat == nil {
		s.done(w, r, fmt.Errorf("chat isn't enabled"), "/")
		return
	}
	s.suggestionDone(w, r, s.Chat.DismissSuggestion(s.actor(r), pathID(r, "id")))
}

// suggestionDone re-renders the chat in place for htmx (the side panel), or redirects back to
// the full chat page otherwise.
func (s *Server) suggestionDone(w http.ResponseWriter, r *http.Request, err error) {
	if r.Header.Get("HX-Request") != "true" {
		s.done(w, r, err, "/")
		return
	}
	a := s.actor(r)
	cid := chatCampaignFromBase(r.FormValue("back"))
	if cid != 0 {
		if _, cerr := s.Svc.Campaign(a, cid); cerr != nil {
			cid = 0
		}
	}
	d := s.buildChatPage(a, cid, r)
	if err != nil {
		d.Error = friendly(err)
	}
	s.partial(w, "chat", "chat-body", d)
}

// chatCampaignFromBase reads the campaign id out of a chat base ("/c/3/chat" → 3, "/chat" → 0).
func chatCampaignFromBase(base string) uint {
	rest, ok := strings.CutPrefix(base, "/c/")
	if !ok {
		return 0
	}
	id, _, _ := strings.Cut(rest, "/")
	n, _ := strconv.ParseUint(id, 10, 64)
	return uint(n)
}
