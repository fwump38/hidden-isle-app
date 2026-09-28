package web

import (
	"encoding/json"
	"html"
	"net/url"
	"strings"
	"testing"
)

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

	wireChatWith(t, srv, suggestResp(map[string]any{"word": "Proud", "why": "fits"}, map[string]any{"word": "Stubborn", "why": "also"}))
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
	var msgs int64
	svc.DB.Table("chat_messages").Count(&msgs)
	if msgs != 0 {
		t.Errorf("creation wrote %d chat messages; it shouldn't use the chat at all", msgs)
	}

	wireChatWith(t, srv, suggestResp(map[string]any{"ability": "Familiar", "why": "a companion"}, map[string]any{"ability": "FIREBALL", "why": "made up"}))
	_, body = st.postBody("Ana", "/agents/1/wizard/suggest", url.Values{"kind": {"abilities"}})
	if !strings.Contains(body, `name="ability.add" value="familiar"`) || strings.Contains(body, "FIREBALL") {
		t.Errorf("ability suggestions should be the class's own, pickable: %s", body)
	}

	wireChatWith(t, srv, suggestResp(map[string]any{"points": "Study +2, Nonsense +1, Slip +1", "why": "bookish"}))
	_, body = st.postBody("Ana", "/agents/1/wizard/suggest", url.Values{"kind": {"skills"}, "skill.Study": {"0"}})
	body = html.UnescapeString(body)
	if !strings.Contains(body, `"skill.Study":"2"`) || !strings.Contains(body, `"skill.Slip":"1"`) || strings.Contains(body, "Nonsense") {
		t.Errorf("skill suggestions should become new totals for real skills: %s", body)
	}

	wireChatWith(t, srv, suggestResp(map[string]any{"class": "Prowler", "why": "sneaky"}, map[string]any{"class": "Wizard", "why": "no such class"}))
	_, body = st.postBody("Ana", "/agents/create/suggest", url.Values{"kind": {"class"}, "hint": {"a sneaky thief"}})
	if !strings.Contains(body, `name="class" value="prowler"`) || strings.Contains(body, "Wizard") {
		t.Errorf("class suggestions should pick a real class on the class page: %s", body)
	}

	// If every option Claude offers turns out not to match a real class, the player must see an
	// error, not a silently empty box (a spinner that stops with nothing to show).
	wireChatWith(t, srv, suggestResp(map[string]any{"class": "Wizard", "why": "no such class"}))
	_, body = st.postBody("Ana", "/agents/create/suggest", url.Values{"kind": {"class"}, "hint": {"a sneaky thief"}})
	if !strings.Contains(body, "Nothing usable came back") {
		t.Errorf("no usable class suggestions should say so, not render empty: %s", body)
	}
}
