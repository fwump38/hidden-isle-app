package web

import (
	"net/url"
	"strings"
	"testing"
)

func TestMentionsRendersEachKind(t *testing.T) {
	body := "Ask @[Ines](agent:5) or her contact @[Mother Agnese](contact:5.9), or @[The Seer](user:1) about @[The Choir](adversary:3) near @[The Harbor](territory:7)."
	got := string(mentions(body, 2))
	want := map[string]string{
		"agent":     `<a class="hi-mention" href="/agents/5">@Ines</a>`,
		"contact":   `<a class="hi-mention" href="/agents/5#contact-9">@Mother Agnese</a>`,
		"user":      `<span class="hi-mention hi-mention-user">@The Seer</span>`,
		"adversary": `<a class="hi-mention" href="/c/2/adversaries#adversary-3">@The Choir</a>`,
		"territory": `<a class="hi-mention" href="/c/2/territories#territory-7">@The Harbor</a>`,
	}
	for kind, frag := range want {
		if !strings.Contains(got, frag) {
			t.Errorf("%s: missing %q in %s", kind, frag, got)
		}
	}
}

func TestMentionsEscapesPlainTextAndNames(t *testing.T) {
	got := string(mentions(`<script>alert(1)</script> @[<b>Evil</b>](agent:5)`, 1))
	if strings.Contains(got, "<script>") || strings.Contains(got, "<b>Evil</b>") {
		t.Errorf("mentions didn't escape untrusted text: %s", got)
	}
	if !strings.Contains(got, "&lt;script&gt;") || !strings.Contains(got, "&lt;b&gt;Evil&lt;/b&gt;") {
		t.Errorf("expected escaped forms in: %s", got)
	}
}

func TestMentionsLeavesUnknownKindOrBadIDPlain(t *testing.T) {
	for _, body := range []string{
		"@[Something](spellbook:5)",   // unknown kind
		"@[Something](agent:notanid)", // bad id
		"@[Broken](contact:5)",        // contact needs agent.contact
	} {
		got := string(mentions(body, 1))
		if strings.Contains(got, "<a") || strings.Contains(got, "<span") {
			t.Errorf("%q should render as plain text, got %s", body, got)
		}
		if !strings.Contains(got, "@Something") && !strings.Contains(got, "@Broken") {
			t.Errorf("%q should still show its display name: %s", body, got)
		}
	}
}

func TestPlainMentionsStripsToDisplayName(t *testing.T) {
	got := plainMentions("Ask @[Ines](agent:5) about it.")
	if got != "Ask @Ines about it." {
		t.Errorf("plainMentions = %q", got)
	}
}

// TestMentionListFiltersAndSearches covers the picker's data source: it returns the Seer,
// members, Agents, their contacts, adversaries and territories, narrows by q, and — the
// permission-critical part — never includes a hidden adversary for a player, only the Seer.
func TestMentionListFiltersAndSearches(t *testing.T) {
	st, _, _ := newSiteWithServer(t)
	ok := func(code int, flash string) {
		t.Helper()
		if code != 303 || flash != "" {
			t.Fatalf("setup post: %d %q", code, flash)
		}
	}
	ok(st.post("Seer", "/campaigns", url.Values{"name": {"Venice"}, "mode": {"group"}}))
	ok(st.post("Seer", "/c/1/members", url.Values{"user_id": {"2"}, "member": {"on"}}))
	ok(st.post("Ana", "/c/1/agents", url.Values{"name": {"Ines"}, "class": {"prowler"}}))
	ok(st.post("Ana", "/agents/1/contacts", url.Values{"set.name": {"Mother Agnese"}, "set.kind": {"Homeland"}}))
	ok(st.post("Seer", "/c/1/r/adversary", url.Values{"set.name": {"Death cult"}}))
	ok(st.post("Seer", "/c/1/r/adversary", url.Values{"set.name": {"Secret foe"}, "set.hidden": {"on"}}))
	ok(st.post("Seer", "/c/1/r/territory", url.Values{"set.name": {"The Harbor"}}))

	_, seerBody := st.get("Seer", "/c/1/mentions?q=")
	for _, want := range []string{"Ines", "Mother Agnese", "Death cult", "Secret foe", "The Harbor", "Ana"} {
		if !strings.Contains(seerBody, want) {
			t.Errorf("Seer's mention list missing %q: %s", want, seerBody)
		}
	}

	_, anaBody := st.get("Ana", "/c/1/mentions?q=")
	if strings.Contains(anaBody, "Secret foe") {
		t.Error("a player's mention list should never include a hidden adversary")
	}
	if !strings.Contains(anaBody, "Death cult") {
		t.Error("a player's mention list should include a non-hidden adversary")
	}

	_, filtered := st.get("Ana", "/c/1/mentions?q=harb")
	if !strings.Contains(filtered, "The Harbor") || strings.Contains(filtered, "Death cult") {
		t.Errorf("q=harb should narrow to The Harbor only: %s", filtered)
	}
}

// TestMentionPickerWiredOnJournal checks the journal's textareas carry data-mentions, and that a
// mention token in a saved entry renders as a link on the page.
func TestMentionPickerWiredOnJournal(t *testing.T) {
	st, _ := newSite(t)
	st.post("Seer", "/campaigns", url.Values{"name": {"Venice"}, "mode": {"group"}})
	st.post("Seer", "/c/1/members", url.Values{"user_id": {"2"}, "member": {"on"}})
	st.post("Ana", "/c/1/agents", url.Values{"name": {"Ines"}, "class": {"prowler"}})

	_, body := st.get("Ana", "/c/1/journal")
	if !strings.Contains(body, `data-mentions="1"`) {
		t.Error("the journal's body textareas should carry data-mentions")
	}

	if code, flash := st.post("Ana", "/c/1/entries", url.Values{
		"kind": {"journal"}, "title": {"Diary"}, "body": {"Talked to @[Ines](agent:1) today."}, "visibility": {"party"},
	}); code != 303 || flash != "" {
		t.Fatalf("save entry: %d %q", code, flash)
	}
	_, body = st.get("Ana", "/c/1/journal")
	if !strings.Contains(body, `<a class="hi-mention" href="/agents/1">@Ines</a>`) {
		t.Errorf("saved mention should render as a link: %s", body)
	}
}
