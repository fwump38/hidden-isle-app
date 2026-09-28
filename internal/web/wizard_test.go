package web

import (
	"encoding/json"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/fwump38/hidden-isle-app/internal/assist"
	"github.com/fwump38/hidden-isle-app/internal/rules"
)

// wireAssistFunc gives srv a working assist.Service backed by a fake Anthropic server running
// handler, so web-layer tests can exercise the suggest and write-assist routes without any
// network use.
func wireAssistFunc(t *testing.T, srv *Server, handler http.HandlerFunc) {
	t.Helper()
	fake := httptest.NewServer(handler)
	t.Cleanup(fake.Close)
	idx, err := rules.New(srv.DB)
	if err != nil {
		t.Fatal(err)
	}
	srv.Assist = assist.New(srv.DB, srv.Svc, srv.Data, idx, assist.Config{APIKey: "test", BaseURL: fake.URL})
}

// wireAssistWith is wireAssistFunc with a fixed raw response body (e.g. a tool_use block).
func wireAssistWith(t *testing.T, srv *Server, body []byte) {
	t.Helper()
	wireAssistFunc(t, srv, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(body)
	})
}

// wireAssistWithCounter is wireAssistWith plus a call counter, for tests that assert a request
// was refused before ever reaching the API.
func wireAssistWithCounter(t *testing.T, srv *Server, calls *int, body []byte) {
	t.Helper()
	var n int32
	wireAssistFunc(t, srv, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&n, 1)
		*calls = int(atomic.LoadInt32(&n))
		w.Header().Set("Content-Type", "application/json")
		w.Write(body)
	})
}

// wireAssistWithCapture is wireAssistWith plus a copy of the last request body it received
// (decoded), for tests that check exactly what context was sent to the model.
func wireAssistWithCapture(t *testing.T, srv *Server, last *map[string]any, body []byte) {
	t.Helper()
	wireAssistFunc(t, srv, func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(last)
		w.Header().Set("Content-Type", "application/json")
		w.Write(body)
	})
}

func suggestResp(options ...map[string]any) []byte {
	b, _ := json.Marshal(map[string]any{
		"id": "msg_1", "type": "message", "role": "assistant", "model": "claude-sonnet-5",
		"content":     []map[string]any{{"type": "tool_use", "id": "toolu_1", "name": "offer_suggestions", "input": map[string]any{"options": options}}},
		"stop_reason": "tool_use",
		"usage":       map[string]any{"input_tokens": 100, "output_tokens": 20},
	})
	return b
}

// newWizardAgent creates an unnamed Agent of class through the step-by-step path, as Ana.
func newWizardAgent(t *testing.T, st *site, class string) {
	t.Helper()
	if code, flash := st.post("Ana", "/agents/create/class", url.Values{"path": {"step"}, "class": {class}, "campaign_id": {"0"}}); code != 303 || flash != "" {
		t.Fatalf("create: %d %q", code, flash)
	}
}

// TestWizardBackButtons: every screen has a Back button to the one before (the first goes back to
// the class page to change class), and a class without magic skips the magic screen both ways.
func TestWizardBackButtons(t *testing.T) {
	st, _ := newSite(t)
	newWizardAgent(t, st, "prowler")
	const w = "/agents/1/wizard?step="
	want := map[string]string{
		"name": "/agents/create/class?agent=1&amp;campaign_id=0", "child": w + "name", "adult": w + "child",
		"burden": w + "adult", "ideal": w + "burden", "abilities": w + "ideal", "skills": w + "abilities",
		"look": w + "skills", "why": w + "look", "homeland": w + "why", "dioscorian": w + "homeland", "done": w + "dioscorian",
	}
	for step, back := range want {
		code, body := st.get("Ana", w+step)
		if code != 200 {
			t.Fatalf("%s: %d", step, code)
		}
		if !strings.Contains(body, `href="`+back+`"><i class="bi bi-arrow-left"></i>`) {
			t.Errorf("%s: no Back button to %s", step, back)
		}
	}
	if _, body := st.get("Ana", "/agents/1/wizard?step=skills"); !strings.Contains(body, `value="/agents/1/wizard?step=look"`) {
		t.Error("a prowler's skills screen should continue to the look screen, skipping magic")
	}
	if _, body := st.get("Ana", "/agents/1/wizard"); strings.Contains(body, ">Magic<") || strings.Contains(body, " Magic\n") {
		t.Error("the stepper shouldn't list magic for a class that doesn't choose it")
	}
}

// TestWizardCardOptions: entering the card you drew shows what the book offers for it, as choices
// that fill in the step's answer.
func TestWizardCardOptions(t *testing.T) {
	st, _ := newSite(t)
	newWizardAgent(t, st, "prowler")

	code, body := st.get("Ana", "/agents/1/wizard/card?for=burden&set.burden_card=page+of+swords")
	if code != 200 || strings.Contains(body, "<html") {
		t.Fatalf("card picker: %d %q", code, body)
	}
	body = html.UnescapeString(body)
	for _, want := range []string{`id="card-burden"`, `value="Page of Swords"`, "TestBurdenPage", `"set.burden":"TestBurdenPage"`} {
		if !strings.Contains(body, want) {
			t.Errorf("card picker missing %q: %s", want, body)
		}
	}
	if _, body := st.get("Ana", "/agents/1/wizard/card?for=burden&set.burden_card=The+Moon+Pie"); !strings.Contains(body, "isn't a vision card") {
		t.Error("an unknown card should say so")
	}
	if code, _ := st.get("Bram", "/agents/1/wizard/card?for=burden&set.burden_card=Page+of+Swords"); code == 200 {
		t.Error("another player shouldn't use Ana's wizard")
	}

	// A saved card shows its options when the step is revisited (e.g. after Back).
	if code, flash := st.post("Ana", "/agents/1", url.Values{"set.name": {"Ines"}, "set.child_card": {"Knight of Cups"}, "set.child_phrase": {"x"}}); code != 303 || flash != "" {
		t.Fatalf("save: %d %q", code, flash)
	}
	if _, body := st.get("Ana", "/agents/1/wizard?step=child"); !strings.Contains(body, "Test childhood phrase (Knight Cups)") {
		t.Error("revisiting a step should show the saved card's phrases")
	}

	// Contacts draw 3 to pick from.
	code, body = st.postBody("Ana", "/agents/1/wizard/draw", url.Values{"for": {"homeland"}})
	if n := strings.Count(html.UnescapeString(body), `"set.card":`); code != 200 || n != 3 {
		t.Errorf("a contact draw should offer 3 cards, got %d: %d %s", n, code, body)
	}
}

func TestWizardNames(t *testing.T) {
	st, _ := newSite(t)
	newWizardAgent(t, st, "prowler")
	if _, body := st.get("Ana", "/agents/1/wizard"); !strings.Contains(body, `<option value="Venice">`) || !strings.Contains(body, `<option value="Dioscoria">`) {
		t.Error("the name screen should offer the book's lands, plus Dioscoria from the homebrew names")
	}
	code, body := st.get("Ana", "/agents/1/wizard/names?name_region=Venice")
	body = html.UnescapeString(body)
	if code != 200 || !strings.Contains(body, `"set.name":"Test `) || !strings.Contains(body, "homebrew supplement") {
		t.Fatalf("names: %d %s", code, body)
	}
	if !strings.Contains(body, `"set.culture":"Venetian"`) {
		t.Error("picking a name from a land should fill in the culture too")
	}
}

// TestWizardSuggest covers Claude's background suggestions: they come back as choices (never a
// chat, never saved), options that aren't in the rules data are dropped, and with the assistant
// off the step says so instead of failing.
func TestWizardSuggest(t *testing.T) {
	st, svc, srv := newSiteWithServer(t)
	newWizardAgent(t, st, "occultist")

	code, body := st.postBody("Ana", "/agents/1/wizard/suggest", url.Values{"kind": {"burden"}})
	if code != 200 || !strings.Contains(html.UnescapeString(body), "isn't set up") {
		t.Fatalf("with the assistant off: %d %q", code, body)
	}
	if _, body := st.get("Ana", "/agents/1/wizard?step=burden"); strings.Contains(body, "More ideas") {
		t.Error("the suggestion box shouldn't show with the assistant off")
	}

	wireAssistWith(t, srv, suggestResp(map[string]any{"word": "Proud", "reason": "fits"}, map[string]any{"word": "Stubborn", "reason": "also"}))
	if _, body := st.get("Ana", "/agents/1/wizard?step=burden"); !strings.Contains(body, "More ideas") {
		t.Error("the suggestion box should show with the assistant on")
	}
	code, body = st.postBody("Ana", "/agents/1/wizard/suggest", url.Values{"kind": {"burden"}, "set.burden_card": {"Page of Swords"}, "exclude": {"Hasty"}})
	body = html.UnescapeString(body)
	if code != 200 || !strings.Contains(body, `"set.burden":"Proud"`) || !strings.Contains(body, `"set.burden":"Stubborn"`) {
		t.Fatalf("burden suggestions: %d %s", code, body)
	}
	for _, ex := range []string{"Hasty", "Proud", "Stubborn"} {
		if !strings.Contains(body, `name="exclude" value="`+ex+`"`) {
			t.Errorf("next request should exclude %q", ex)
		}
	}
	var ag struct{ Burden string }
	svc.DB.Table("agents").Select("burden").Where("id = 1").Scan(&ag)
	if ag.Burden != "" {
		t.Errorf("a suggestion was saved (%q); only the player saves", ag.Burden)
	}

	wireAssistWith(t, srv, suggestResp(map[string]any{"ability": "Familiar", "reason": "a companion"}, map[string]any{"ability": "FIREBALL", "reason": "made up"}))
	_, body = st.postBody("Ana", "/agents/1/wizard/suggest", url.Values{"kind": {"abilities"}})
	if !strings.Contains(body, `name="ability_id" value="familiar"`) || strings.Contains(body, "FIREBALL") {
		t.Errorf("ability suggestions should be the class's own, pickable: %s", body)
	}

	// "Nonsense" isn't a real skill and is dropped; the point it would have spent isn't just
	// lost, it lands on Slip too (the suggestion's other named skill still has room), so the
	// total still uses all 4 points remaining rather than quietly offering only 3.
	wireAssistWith(t, srv, suggestResp(map[string]any{"points": "Study +2, Nonsense +1, Slip +1", "reason": "bookish"}))
	_, body = st.postBody("Ana", "/agents/1/wizard/suggest", url.Values{"kind": {"skills"}, "skill.Study": {"0"}})
	body = html.UnescapeString(body)
	if !strings.Contains(body, `"skill.Study":"2"`) || !strings.Contains(body, `"skill.Slip":"2"`) || strings.Contains(body, "Nonsense") {
		t.Errorf("skill suggestions should become new totals for real skills, topped up to use every point: %s", body)
	}

	// This occultist's class prefills Unleash 1 (fixture); the creation cap is 2/skill. A
	// suggestion of "+2" would put it at 3, over the cap — it must be clamped to what's still
	// legal (+1), not trusted just because the model said so.
	wireAssistWith(t, srv, suggestResp(map[string]any{"points": "Unleash +2", "reason": "fits their temper"}))
	_, body = st.postBody("Ana", "/agents/1/wizard/suggest", url.Values{"kind": {"skills"}})
	body = html.UnescapeString(body)
	if !strings.Contains(body, `"skill.Unleash":"2"`) || strings.Contains(body, "Unleash +2") {
		t.Errorf("a skill suggestion over the creation cap should be clamped, not offered as-is: %s", body)
	}

	// With only 1 of the 7 creation points left unspent (Unleash 1 + Channel 2 prefilled, plus
	// Skirmish 2 + Convince 1 entered but not yet saved), a suggested "+2" elsewhere must be
	// clamped to the 1 point actually remaining, even though Bargain itself is nowhere near cap.
	wireAssistWith(t, srv, suggestResp(map[string]any{"points": "Bargain +2", "reason": "too generous"}))
	_, body = st.postBody("Ana", "/agents/1/wizard/suggest", url.Values{"kind": {"skills"}, "skill.Skirmish": {"2"}, "skill.Convince": {"1"}})
	body = html.UnescapeString(body)
	if !strings.Contains(body, `"skill.Bargain":"1"`) || strings.Contains(body, "Bargain +2") {
		t.Errorf("a skill suggestion shouldn't exceed the points actually remaining: %s", body)
	}

	wireAssistWith(t, srv, suggestResp(map[string]any{"class": "Prowler", "reason": "sneaky"}, map[string]any{"class": "Wizard", "reason": "no such class"}))
	_, body = st.postBody("Ana", "/agents/create/suggest", url.Values{"kind": {"class"}, "hint": {"a sneaky thief"}})
	if !strings.Contains(body, `name="class" value="prowler"`) || strings.Contains(body, "Wizard") {
		t.Errorf("class suggestions should pick a real class on the class page: %s", body)
	}

	// If every option Claude offers turns out not to match a real class, the player must see an
	// error, not a silently empty box (a spinner that stops with nothing to show).
	wireAssistWith(t, srv, suggestResp(map[string]any{"class": "Wizard", "reason": "no such class"}))
	_, body = st.postBody("Ana", "/agents/create/suggest", url.Values{"kind": {"class"}, "hint": {"a sneaky thief"}})
	if !strings.Contains(body, "Nothing usable came back") {
		t.Errorf("no usable class suggestions should say so, not render empty: %s", body)
	}
}

// TestWizardSuggestDrawsACardWhenNoneChosenYet is a regression test: asking for suggestions on a
// card-driven step (child, adult, burden, ideal, a contact) before the player has drawn or entered
// a card used to send Claude no card at all, so "in the spirit of the drawn card" suggestions were
// invented from nothing. It should draw one itself, the same digital fallback "Draw for me" uses,
// and fill it into the form too when a suggestion inspired by it gets picked.
func TestWizardSuggestDrawsACardWhenNoneChosenYet(t *testing.T) {
	st, _, srv := newSiteWithServer(t)
	newWizardAgent(t, st, "prowler")

	var sentBody map[string]any
	wireAssistFunc(t, srv, func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&sentBody)
		w.Header().Set("Content-Type", "application/json")
		w.Write(suggestResp(map[string]any{"word": "Proud", "reason": "fits"}))
	})
	_, body := st.postBody("Ana", "/agents/1/wizard/suggest", url.Values{"kind": {"burden"}})
	body = html.UnescapeString(body)
	raw, _ := json.Marshal(sentBody)
	if !strings.Contains(string(raw), "Drawn card:") {
		t.Fatalf("no card was drawn for the request: %s", raw)
	}
	if !strings.Contains(body, `"set.burden":"Proud"`) || !strings.Contains(body, `"set.burden_card":"`) {
		t.Errorf("picking the suggestion should fill in both the word and the card drawn for it: %s", body)
	}
	// The card wizardContext drew must also be shown right away, via an out-of-band swap into the
	// existing #card-burden element, not just implied by suggestion text the player can't verify.
	if !strings.Contains(body, `id="card-burden"`) || !strings.Contains(body, `hx-swap-oob="true"`) {
		t.Errorf("a freshly-drawn card should be shown immediately alongside the suggestions: %s", body)
	}

	// homeland and dioscorian share the "contact" suggestion kind and both fill "set.card", not a
	// step-specific field, so wizardContext's drawn card has to be mapped back to that explicitly.
	wireAssistWith(t, srv, suggestResp(map[string]any{"name": "Marco", "land": "Venice", "description": "a fisherman", "reason": "fits"}))
	_, body = st.postBody("Ana", "/agents/1/wizard/suggest", url.Values{"kind": {"contact"}, "set.kind": {"Homeland"}})
	body = html.UnescapeString(body)
	if !strings.Contains(body, `"set.name":"Marco"`) || !strings.Contains(body, `"set.card":"`) {
		t.Errorf("a contact suggestion with no card yet should fill in the card it drew: %s", body)
	}
	if !strings.Contains(body, `id="card-homeland"`) || !strings.Contains(body, `hx-swap-oob="true"`) {
		t.Errorf("a homeland contact's freshly-drawn card should be shown immediately too: %s", body)
	}
}

// TestWizardContactSuggestDrawsThreeCards is a regression test: a homeland or dioscorian contact
// draws 3 cards to pick from (p. 41), same as "Draw 3 for me" — asking for suggestions before the
// player has drawn or entered a card used to draw only 1 and base every suggestion on it, instead
// of drawing 3 and suggesting one contact per card.
func TestWizardContactSuggestDrawsThreeCards(t *testing.T) {
	st, _, srv := newSiteWithServer(t)
	newWizardAgent(t, st, "prowler")

	var mu sync.Mutex
	var bodies []map[string]any
	names := []string{"Marco", "Elena", "Bianca"}
	wireAssistFunc(t, srv, func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		json.NewDecoder(r.Body).Decode(&b)
		mu.Lock()
		i := len(bodies)
		bodies = append(bodies, b)
		mu.Unlock()
		name := names[i%len(names)]
		w.Header().Set("Content-Type", "application/json")
		w.Write(suggestResp(map[string]any{"name": name, "land": "Venice", "description": "a " + name, "reason": "fits"}))
	})

	_, body := st.postBody("Ana", "/agents/1/wizard/suggest", url.Values{"kind": {"contact"}, "set.kind": {"Homeland"}})
	body = html.UnescapeString(body)

	if len(bodies) != 3 {
		t.Fatalf("a blank contact card should draw 3 cards and ask for one suggestion per card (3 calls), got %d", len(bodies))
	}
	cards := map[string]bool{}
	for _, b := range bodies {
		raw, _ := json.Marshal(b)
		s := string(raw)
		start := strings.Index(s, "Their card: ")
		if start < 0 {
			t.Fatalf("a call for a drawn contact card should describe it: %s", s)
		}
		s = s[start+len("Their card: "):]
		end := strings.Index(s, " — ") // " — ", cardLine's separator before the card's meaning
		if end < 0 {
			t.Fatalf("couldn't find the card's name in its context line: %s", s)
		}
		cards[s[:end]] = true
	}
	if len(cards) != 3 {
		t.Errorf("the 3 calls should each be about a different drawn card, got %v", cards)
	}
	for _, name := range names {
		if !strings.Contains(body, name) {
			t.Errorf("a suggestion for each drawn card should be offered (missing %q): %s", name, body)
		}
	}
	// Only count fills in the suggestion choices themselves, before the OOB "pick one of the 3
	// drawn cards directly" box, which also fills "set.card" once per card it lists.
	choices, _, _ := strings.Cut(body, `id="card-homeland"`)
	if n := strings.Count(choices, `"set.card":"`); n != 3 {
		t.Errorf("each of the 3 suggestions should fill in its own drawn card, got %d: %s", n, choices)
	}
	if !strings.Contains(body, `id="card-homeland"`) || !strings.Contains(body, `hx-swap-oob="true"`) {
		t.Errorf("all 3 freshly-drawn cards should be shown immediately, pick-one style: %s", body)
	}
	if !strings.Contains(body, "You drew these") {
		t.Errorf("3 drawn cards should render as a pick-one draw, not a single card: %s", body)
	}
}

// TestNoChatEverywhere is a regression test for removing the in-app chat: no page should carry
// the old chat drawer, floating button or panel markup, whether or not AI assistance is
// configured, and the old chat routes should no longer exist at all.
func TestNoChatEverywhere(t *testing.T) {
	st, _, srv := newSiteWithServer(t)
	wireAssistWith(t, srv, suggestResp(map[string]any{"word": "Proud", "reason": "fits"}))

	for _, path := range []string{"/", "/agents/create", "/admin"} {
		_, body := st.get("Seer", path)
		for _, marker := range []string{"chat-drawer", "hi-chat-fab", "data-chat-toggle", `id="chat"`} {
			if strings.Contains(body, marker) {
				t.Errorf("%s: unexpected chat markup %q", path, marker)
			}
		}
	}

	for _, path := range []string{"/chat", "/chat/panel", "/chat/send"} {
		code, _ := st.get("Seer", path)
		if code != http.StatusNotFound {
			t.Errorf("%s: code = %d, want 404 (the chat routes are gone)", path, code)
		}
	}
}

// TestAIControlsHiddenWithoutAssist covers the "gracefully hide, not disable" requirement: with
// no ANTHROPIC_API_KEY (srv.Assist == nil), no page offers a Suggest control, and the suggest
// endpoints refuse cleanly instead of panicking on a nil Service.
func TestAIControlsHiddenWithoutAssist(t *testing.T) {
	st, _ := newSite(t) // srv.Assist is nil: no key configured

	if _, body := st.get("Ana", "/agents/create"); strings.Contains(body, "Suggest a class") {
		t.Error("the class page shouldn't offer suggestions with the assistant off")
	}
	newWizardAgent(t, st, "occultist")
	if _, body := st.get("Ana", "/agents/1/wizard?step=burden"); strings.Contains(body, "More ideas") {
		t.Error("the wizard shouldn't offer suggestions with the assistant off")
	}
	if code, body := st.postBody("Ana", "/agents/1/wizard/suggest", url.Values{"kind": {"burden"}}); code != 200 || !strings.Contains(html.UnescapeString(body), "isn't set up") {
		t.Errorf("suggest with no assistant should say so, not error: %d %q", code, body)
	}
}
