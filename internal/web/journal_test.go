package web

import (
	"net/url"
	"strings"
	"testing"

	"github.com/fwump38/hidden-isle-app/internal/db"
)

// TestJournalVisibilityAndDraft covers the merged "Who can see this" select and "Save as draft"
// checkbox: draft only matters for Everyone, every other visibility is shared right away, and
// the kind selector no longer offers "Note".
func TestJournalVisibilityAndDraft(t *testing.T) {
	st, svc := newSite(t)
	if code, flash := st.post("Seer", "/campaigns", url.Values{"name": {"Venice"}, "mode": {"group"}}); code != 303 || flash != "" {
		t.Fatalf("campaign: %d %q", code, flash)
	}
	if code, flash := st.post("Seer", "/c/1/members", url.Values{"user_id": {"2"}, "member": {"on"}}); code != 303 || flash != "" {
		t.Fatalf("member: %d %q", code, flash)
	}

	_, body := st.get("Ana", "/c/1/journal")
	if strings.Contains(body, `value="note"`) {
		t.Error("the kind selector should no longer offer Note")
	}

	// Everyone + draft: not published.
	if code, flash := st.post("Ana", "/c/1/entries", url.Values{
		"kind": {"journal"}, "title": {"Draft recap"}, "body": {"x"}, "visibility": {"party"}, "draft": {"on"},
	}); code != 303 || flash != "" {
		t.Fatalf("party draft: %d %q", code, flash)
	}
	var e1 db.Entry
	svc.DB.Where("title = ?", "Draft recap").First(&e1)
	if e1.Published {
		t.Error("Everyone + draft should not be published")
	}

	// Everyone, no draft: published right away.
	if code, flash := st.post("Ana", "/c/1/entries", url.Values{
		"kind": {"journal"}, "title": {"Shared now"}, "body": {"y"}, "visibility": {"party"},
	}); code != 303 || flash != "" {
		t.Fatalf("party no draft: %d %q", code, flash)
	}
	var e2 db.Entry
	svc.DB.Where("title = ?", "Shared now").First(&e2)
	if !e2.Published {
		t.Error("Everyone without draft should be published immediately")
	}

	// Owner visibility: always published, draft or not (the checkbox doesn't even apply).
	if code, flash := st.post("Ana", "/c/1/entries", url.Values{
		"kind": {"journal"}, "title": {"For the Seer"}, "body": {"z"}, "visibility": {"owner"}, "draft": {"on"},
	}); code != 303 || flash != "" {
		t.Fatalf("owner: %d %q", code, flash)
	}
	var e3 db.Entry
	svc.DB.Where("title = ?", "For the Seer").First(&e3)
	if !e3.Published {
		t.Error("owner visibility should always be published; draft only means anything for Everyone")
	}
}

// TestQuickNoteCreatesASeerNote covers the Play page's quick note: it now creates a SeerNote,
// not an Entry, and stays Seer-only through the normal seer_note permission (not a visibility
// flag on an entry).
func TestQuickNoteCreatesASeerNote(t *testing.T) {
	st, svc := newSite(t)
	st.post("Seer", "/campaigns", url.Values{"name": {"Venice"}, "mode": {"group"}})

	if code, flash := st.post("Seer", "/c/1/r/seer_note", url.Values{"set.title": {"Table note"}, "set.body": {"the mayor flinched"}, "back": {"/c/1/play"}}); code != 303 || flash != "" {
		t.Fatalf("quick note: %d %q", code, flash)
	}
	var n db.SeerNote
	if err := svc.DB.Where("body = ?", "the mayor flinched").First(&n).Error; err != nil {
		t.Fatalf("quick note wasn't saved as a SeerNote: %v", err)
	}
	var entries int64
	svc.DB.Model(&db.Entry{}).Where("body = ?", "the mayor flinched").Count(&entries)
	if entries != 0 {
		t.Error("the quick note shouldn't also exist as an Entry")
	}

	_, playBody := st.get("Seer", "/c/1/play")
	if !strings.Contains(playBody, `action="/c/1/r/seer_note"`) {
		t.Error("the Play page's quick note form should post to the seer_note record route")
	}
}
