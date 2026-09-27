package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/fwump38/hidden-isle-app/internal/chat"
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
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(textResp(reply))
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
}
