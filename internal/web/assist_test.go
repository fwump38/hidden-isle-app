package web

import (
	"encoding/json"
	"html"
	"net/url"
	"strings"
	"testing"
)

// assistWorld seeds a campaign with both public and Seer-only content, so tests can check what
// the writing assistant's brief does and doesn't include.
func assistWorld(t *testing.T) (*site, *Server) {
	t.Helper()
	st, _, srv := newSiteWithServer(t)
	ok := func(code int, flash string) {
		t.Helper()
		if code != 303 || flash != "" {
			t.Fatalf("setup post: %d %q", code, flash)
		}
	}
	ok(st.post("Seer", "/campaigns", url.Values{"name": {"Venice"}, "mode": {"group"}}))
	ok(st.post("Seer", "/c/1/members", url.Values{"user_id": {"2"}, "member": {"on"}}))
	ok(st.post("Seer", "/c/1/members", url.Values{"user_id": {"3"}, "member": {"on"}}))
	ok(st.post("Ana", "/c/1/agents", url.Values{"name": {"Ines"}, "class": {"prowler"}}))
	ok(st.post("Seer", "/c/1/r/adversary", url.Values{"set.name": {"Death cult"}, "set.track_length": {"8"}, "set.secrets": {"SECRET-LEADER-IS-THE-MAYOR"}}))
	return st, srv
}

func assistToolResp(text string) []byte {
	b, _ := json.Marshal(map[string]any{
		"id": "msg_1", "type": "message", "role": "assistant", "model": "claude-sonnet-5",
		"content":     []map[string]any{{"type": "tool_use", "id": "toolu_1", "name": "offer_text", "input": map[string]any{"text": text}}},
		"stop_reason": "tool_use",
		"usage":       map[string]any{"input_tokens": 100, "output_tokens": 20},
	})
	return b
}

// TestAssistWriteEnhanceAndApply covers the happy path end to end: a signed-in campaign member
// enhancing their own journal entry gets back a preview with Replace/Append/Discard.
func TestAssistWriteEnhanceAndApply(t *testing.T) {
	st, srv := assistWorld(t)
	wireAssistWith(t, srv, assistToolResp("We slipped past the guards and found the ledger."))

	code, body := st.postBody("Ana", "/assist/write", url.Values{
		"field": {"journal"}, "mode": {"enhance"}, "text": {"we got past the guards"},
		"campaign_id": {"1"}, "agent_id": {"0"}, "session_id": {"0"}, "target": {"body"},
	})
	if code != 200 || !strings.Contains(body, "We slipped past the guards") {
		t.Fatalf("enhance: %d %q", code, body)
	}
	for _, want := range []string{`data-assist-apply="replace"`, `data-assist-apply="append"`, `data-assist-apply="discard"`} {
		if !strings.Contains(body, want) {
			t.Errorf("preview missing %q: %s", want, body)
		}
	}
}

// TestAssistWriteSeerOnlyGate covers a player asking for a Seer-only field: refused with no API
// call at all.
func TestAssistWriteSeerOnlyGate(t *testing.T) {
	st, srv := assistWorld(t)
	var calls int
	wireAssistWithCounter(t, srv, &calls, assistToolResp("shouldn't be reached"))

	code, body := st.postBody("Ana", "/assist/write", url.Values{
		"field": {"session_prep"}, "mode": {"enhance"}, "text": {"the cast"}, "campaign_id": {"1"},
	})
	if code != 200 || !strings.Contains(body, "Only the Seer") {
		t.Fatalf("player asking for a Seer-only field: %d %q", code, body)
	}
	if calls != 0 {
		t.Errorf("calls = %d, want 0 (should refuse before calling the API)", calls)
	}
}

// TestAssistWriteLeakGuard is the leak-guard regression test: the Seer enhancing a field that
// players will read (Public) never sends adversary secrets to the model, even though the Seer
// can otherwise see them; the same field with Public off does include them.
func TestAssistWriteLeakGuard(t *testing.T) {
	st, srv := assistWorld(t)
	var lastBody map[string]any
	wireAssistWithCapture(t, srv, &lastBody, assistToolResp("Open threads, tidied up."))

	// campaign_open_threads is Public: the adversary's secret must never appear in the request.
	code, _ := st.postBody("Seer", "/assist/write", url.Values{
		"field": {"campaign_open_threads"}, "mode": {"draft"}, "campaign_id": {"1"},
	})
	if code != 200 {
		t.Fatalf("draft open threads: %d", code)
	}
	raw, _ := json.Marshal(lastBody["messages"])
	if strings.Contains(string(raw), "SECRET-LEADER-IS-THE-MAYOR") {
		t.Errorf("a Public field leaked an adversary secret to the model: %s", raw)
	}
	if !strings.Contains(string(raw), "Death cult") {
		t.Errorf("a Public field should still see the adversary's name (not Seer-only): %s", raw)
	}

	// session_prep is Seer-only and not Public: the Seer enhancing it may see the secret.
	code, _ = st.postBody("Seer", "/assist/write", url.Values{
		"field": {"session_prep"}, "mode": {"enhance"}, "text": {"the cast"}, "campaign_id": {"1"},
	})
	if code != 200 {
		t.Fatalf("enhance session prep: %d", code)
	}
	raw, _ = json.Marshal(lastBody["messages"])
	if !strings.Contains(string(raw), "SECRET-LEADER-IS-THE-MAYOR") {
		t.Errorf("a Seer-only field should still see adversary secrets: %s", raw)
	}
}

// TestAssistWriteNeedsAgentAccess covers a NeedsAgent field: another player can't enhance a
// field on someone else's Agent.
func TestAssistWriteNeedsAgentAccess(t *testing.T) {
	st, srv := assistWorld(t)
	wireAssistWith(t, srv, assistToolResp("shouldn't be reached"))
	code, _ := st.postBody("Bram", "/assist/write", url.Values{
		"field": {"agent_look"}, "mode": {"enhance"}, "text": {"tall"}, "agent_id": {"1"},
	})
	if code != 403 {
		t.Errorf("code = %d, want 403 (Bram doesn't own Ines)", code)
	}
}

// TestAssistWriteWithoutAssistConfigured covers the no-API-key case: a clean message, not an
// error page.
func TestAssistWriteWithoutAssistConfigured(t *testing.T) {
	st, _ := assistWorld(t)
	code, body := st.postBody("Ana", "/assist/write", url.Values{"field": {"journal"}, "mode": {"enhance"}, "text": {"hi"}, "campaign_id": {"1"}})
	if code != 200 || !strings.Contains(html.UnescapeString(body), "isn't set up") {
		t.Errorf("without a key: %d %q", code, body)
	}
}

// TestWriteAssistMarkupAcrossPages checks the write-assist boxes wired into the app's free-form
// fields: present (with the right field key) when the assistant is configured, and completely
// absent — not just disabled — when it isn't, on every page that has one.
func TestWriteAssistMarkupAcrossPages(t *testing.T) {
	st, _, srv := newSiteWithServer(t)
	ok := func(code int, flash string) {
		t.Helper()
		if code != 303 || flash != "" {
			t.Fatalf("setup post: %d %q", code, flash)
		}
	}
	ok(st.post("Seer", "/campaigns", url.Values{"name": {"Venice"}, "mode": {"group"}}))
	ok(st.post("Seer", "/c/1/members", url.Values{"user_id": {"2"}, "member": {"on"}}))
	ok(st.post("Ana", "/c/1/agents", url.Values{"name": {"Ines"}, "class": {"prowler"}}))
	ok(st.post("Seer", "/c/1/r/adversary", url.Values{"set.name": {"Death cult"}, "set.track_length": {"8"}}))
	ok(st.post("Seer", "/c/1/r/territory", url.Values{"set.name": {"The Harbor"}}))
	ok(st.post("Seer", "/c/1/r/seer_note", url.Values{"set.title": {"Twists"}}))
	ok(st.post("Seer", "/c/1/r/session", url.Values{"set.title": {"Arrival"}}))

	pages := map[string][]string{
		"/c/1/journal":     {"journal"},
		"/c/1/sessions/1":  {"session_summary", "session_prep", "session_divination", "session_next_time", "session_entry"},
		"/c/1/play":        {"handout", "seer_note"},
		"/c/1/adversaries": {"adversary_plot", "adversary_motivation", "adversary_members", "adversary_secrets"},
		"/c/1/territories": {"territory_events", "territory_contacts", "territory_notes"},
		"/c/1/notes":       {"seer_note"},
		"/c/1/settings":    {"campaign_table", "campaign_open_threads"},
		"/agents/1":        {"agent_look", "agent_why", "agent_notes", "ability_text", "contact_description", "agent_history"},
	}

	// Without a key: no page mentions the assistant at all.
	for path := range pages {
		_, body := st.get("Seer", path)
		if strings.Contains(body, "hi-assist") {
			t.Errorf("%s: hi-assist markup present with no ANTHROPIC_API_KEY", path)
		}
	}

	wireAssistWith(t, srv, assistToolResp("x"))
	for path, fields := range pages {
		_, body := st.get("Seer", path)
		for _, f := range fields {
			if !strings.Contains(body, `data-assist-field="`+f+`"`) {
				t.Errorf("%s: missing write-assist for field %q", path, f)
			}
		}
	}
}

func suggestOptResp(options ...map[string]any) []byte {
	b, _ := json.Marshal(map[string]any{
		"id": "msg_1", "type": "message", "role": "assistant", "model": "claude-sonnet-5",
		"content":     []map[string]any{{"type": "tool_use", "id": "toolu_1", "name": "offer_suggestions", "input": map[string]any{"options": options}}},
		"stop_reason": "tool_use",
		"usage":       map[string]any{"input_tokens": 100, "output_tokens": 20},
	})
	return b
}

// TestSeerSuggestAdversary covers the Seer's own suggestion box end to end: it renders choice
// buttons that fill the new-adversary form's fields.
func TestSeerSuggestAdversary(t *testing.T) {
	st, srv := assistWorld(t)
	wireAssistWith(t, srv, suggestOptResp(map[string]any{
		"name": "The Choir", "leader": "Brother Anselm", "plot": "smuggling", "motivation": "profit", "members": "Anselm — greedy", "why": "fits the docks",
	}))
	code, body := st.postBody("Seer", "/c/1/suggest", url.Values{"kind": {"adversary"}, "hint": {"docks"}})
	if code != 200 {
		t.Fatalf("suggest: %d %q", code, body)
	}
	for _, want := range []string{`&#34;set.name&#34;:&#34;The Choir&#34;`, `&#34;set.plot&#34;:&#34;smuggling&#34;`, "fits the docks"} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in %s", want, body)
		}
	}
}

// TestSeerSuggestRequiresSeer covers a player trying the Seer's own suggestion box: refused, no
// API call.
func TestSeerSuggestRequiresSeer(t *testing.T) {
	st, srv := assistWorld(t)
	var calls int
	wireAssistWithCounter(t, srv, &calls, suggestOptResp(map[string]any{"name": "x"}))
	code, body := st.postBody("Ana", "/c/1/suggest", url.Values{"kind": {"adversary"}})
	if code != 200 || !strings.Contains(body, "Only the Seer") {
		t.Fatalf("player suggest: %d %q", code, body)
	}
	if calls != 0 {
		t.Errorf("calls = %d, want 0", calls)
	}
}

// TestSeerSuggestRejectsUnknownKind covers a kind outside seerKinds (e.g. a player-only wizard
// kind like "class") being refused rather than silently forwarded to the model.
func TestSeerSuggestRejectsUnknownKind(t *testing.T) {
	st, srv := assistWorld(t)
	var calls int
	wireAssistWithCounter(t, srv, &calls, suggestOptResp(map[string]any{"name": "x"}))
	code, body := st.postBody("Seer", "/c/1/suggest", url.Values{"kind": {"class"}})
	if code != 200 || !strings.Contains(body, "Unknown kind") {
		t.Fatalf("unknown kind: %d %q", code, body)
	}
	if calls != 0 {
		t.Errorf("calls = %d, want 0", calls)
	}
}

// TestSeerSuggestBoxesOnPages checks the Seer's own suggest boxes are wired into the campaign
// pages and gated the same way write-assist is: present with a key, absent without one.
func TestSeerSuggestBoxesOnPages(t *testing.T) {
	st, _, srv := newSiteWithServer(t)
	ok := func(code int, flash string) {
		t.Helper()
		if code != 303 || flash != "" {
			t.Fatalf("setup post: %d %q", code, flash)
		}
	}
	ok(st.post("Seer", "/campaigns", url.Values{"name": {"Venice"}, "mode": {"group"}}))
	ok(st.post("Seer", "/c/1/r/territory", url.Values{"set.name": {"The Harbor"}}))

	pages := map[string][]string{
		"/c/1/adversaries": {"adversary"},
		"/c/1/clocks":      {"clock"},
		"/c/1/sessions":    {"session"},
		"/c/1/territories": {"territory_event"},
		"/c/1/play":        {"handout"},
	}
	for path := range pages {
		_, body := st.get("Seer", path)
		if strings.Contains(body, `hx-post="/c/1/suggest"`) {
			t.Errorf("%s: suggest box present with no ANTHROPIC_API_KEY", path)
		}
	}

	wireAssistWith(t, srv, suggestOptResp(map[string]any{"name": "x"}))
	for path, kinds := range pages {
		_, body := st.get("Seer", path)
		for _, k := range kinds {
			if !strings.Contains(body, `name="kind" value="`+k+`"`) {
				t.Errorf("%s: missing suggest box for kind %q", path, k)
			}
		}
	}
}
