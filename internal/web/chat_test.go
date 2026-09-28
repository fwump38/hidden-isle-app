package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/fwump38/hidden-isle-app/internal/chat"
	"github.com/fwump38/hidden-isle-app/internal/db"
	"github.com/fwump38/hidden-isle-app/internal/rules"
)

func textResp(text string) []byte {
	b, _ := json.Marshal(map[string]any{
		"id": "msg_1", "type": "message", "role": "assistant", "model": "claude-sonnet-5",
		"content": []map[string]any{{"type": "text", "text": text}}, "stop_reason": "end_turn",
		"usage": map[string]any{"input_tokens": 100, "output_tokens": 20},
	})
	return b
}

// wireChat gives srv a working chat.Service backed by a fake Anthropic server that always
// replies with reply, so web-layer tests can exercise the chat routes without any network use.
func wireChat(t *testing.T, srv *Server, reply string) {
	t.Helper()
	wireChatWith(t, srv, textResp(reply))
}

// wireChatWith is wireChat with a fixed raw response body (e.g. a tool_use block).
func wireChatWith(t *testing.T, srv *Server, body []byte) {
	t.Helper()
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(body)
	}))
	t.Cleanup(fake.Close)
	idx, err := rules.New(srv.DB)
	if err != nil {
		t.Fatal(err)
	}
	srv.Chat = chat.New(srv.DB, srv.Svc, srv.Data, idx, chat.Config{APIKey: "test", BaseURL: fake.URL})
}

// TestChatWorksOutsideACampaign covers the correction that character-creation chat must work
// before a player has joined any campaign: the bare /chat routes use campaign 0 throughout.
func TestChatWorksOutsideACampaign(t *testing.T) {
	st, _, srv := newSiteWithServer(t)

	code, body := st.get("Ana", "/chat")
	if code != 200 || !strings.Contains(body, "isn't set up yet") {
		t.Fatalf("expected the disabled message before chat is wired up; got %d %q", code, body)
	}

	wireChat(t, srv, "Let's start with your class.")

	code, body = st.get("Ana", "/chat?ask=Help+me+pick+a+class")
	if code != 200 || !strings.Contains(body, "Help me pick a class") {
		t.Fatalf("prefill not shown: %d %q", code, body)
	}

	code, body = st.postBody("Ana", "/chat/send", url.Values{"message": {"What class fits a sneaky character?"}})
	if code != 200 || (!strings.Contains(body, "Let&#39;s start with your class.") && !strings.Contains(body, "Let's start with your class.")) {
		t.Fatalf("send: %d %q", code, body)
	}

	code, body = st.get("Bram", "/chat/with/1")
	if code == 200 && !strings.Contains(strings.ToLower(body), "only the seer") {
		t.Fatalf("Bram shouldn't be able to read another player's chat: %d %q", code, body)
	}
}

// TestChatNavHiddenWhenDisabled matches the earlier fix: the Chat entry points must not appear
// at all when no ANTHROPIC_API_KEY is configured, not just show a "not set up" page.
func TestChatNavHiddenWhenDisabled(t *testing.T) {
	st, _ := newSite(t)
	_, body := st.get("Ana", "/")
	if strings.Contains(body, `href="/chat"`) {
		t.Error("the top nav Chat link should be hidden when chat is off")
	}
}

func TestChatNavShownWhenEnabled(t *testing.T) {
	st, _, srv := newSiteWithServer(t)
	wireChat(t, srv, "hi")
	_, body := st.get("Ana", "/")
	if !strings.Contains(body, `href="/chat"`) {
		t.Error("the top nav Chat link should show once chat is configured")
	}
	if !strings.Contains(body, `id="chat-drawer"`) || !strings.Contains(body, `data-chat-base="/chat"`) {
		t.Error("every page should carry the chat side panel and which chat it belongs to")
	}
}

// TestChatSidePanel covers the chat as a panel beside the app: the panel URL returns just the
// chat (no page around it), campaign pages point the panel at that campaign's chat, and Apply on
// a suggested change re-renders the chat in place and tells the page beside it to refresh.
func TestChatSidePanel(t *testing.T) {
	st, svc, srv := newSiteWithServer(t)
	wireChat(t, srv, "Sure.")
	if code, flash := st.post("Seer", "/campaigns", url.Values{"name": {"Venice"}, "mode": {"group"}}); code != 303 || flash != "" {
		t.Fatalf("campaign: %d %q", code, flash)
	}
	if code, flash := st.post("Seer", "/c/1/members", url.Values{"user_id": {"2"}, "member": {"on"}}); code != 303 || flash != "" {
		t.Fatalf("member: %d %q", code, flash)
	}

	code, body := st.get("Ana", "/chat/panel?ask=What+is+a+burden")
	if code != 200 || strings.Contains(body, "<html") || !strings.Contains(body, `id="chat-body"`) || !strings.Contains(body, "What is a burden") {
		t.Fatalf("panel should be just the chat, pre-filled: %d %q", code, body)
	}
	if _, body := st.get("Ana", "/c/1"); !strings.Contains(body, `data-chat-base="/c/1/chat"`) {
		t.Error("a campaign page should point the panel at that campaign's chat")
	}
	if code, _ := st.get("Bram", "/c/1/chat/panel"); code == 200 {
		t.Error("a non-member shouldn't get a campaign's chat panel")
	}

	if code, flash := st.post("Ana", "/c/1/agents", url.Values{"name": {"Ines"}, "class": {"prowler"}}); code != 303 || flash != "" {
		t.Fatalf("agent: %d %q", code, flash)
	}
	th, err := srv.Chat.Thread(2, 1)
	if err != nil {
		t.Fatal(err)
	}
	sug := db.ChatSuggestion{ThreadID: th.ID, AgentID: 1, Summary: "Set look", Fields: `{"look":"Sharp-eyed"}`, Status: "pending"}
	if err := svc.DB.Create(&sug).Error; err != nil {
		t.Fatal(err)
	}
	code, body, hdr := st.htmxPost("Ana", "/chat/suggest/1/apply", url.Values{"back": {"/c/1/chat"}})
	if code != 200 || !strings.Contains(body, `id="chat-body"`) || strings.Contains(body, "Set look") {
		t.Fatalf("htmx apply should re-render the chat without the applied card: %d %q", code, body)
	}
	if hdr.Get("HX-Trigger") != "agentChanged" {
		t.Errorf("HX-Trigger = %q, want agentChanged so the sheet beside the panel refreshes", hdr.Get("HX-Trigger"))
	}
	var ag db.Agent
	svc.DB.First(&ag, 1)
	if ag.Look != "Sharp-eyed" {
		t.Errorf("look = %q, the suggestion wasn't applied", ag.Look)
	}
}
