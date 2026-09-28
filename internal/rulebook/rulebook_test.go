package rulebook

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fwump38/hidden-isle-app/internal/gamedata"
)

func write(t *testing.T, dir, rel, body string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func testSnapshot(t *testing.T) *gamedata.Snapshot {
	dir := t.TempDir()
	write(t, dir, "markdown/rules/01_core.md", "---\ntitle: Core Rules\nsource: Test Rulebook 1.0.pdf\npages: 1-2\n---\n\n<!-- p.1 -->\n# Core\n\nDraw cards (see p. 2).\n\n<!-- p.2 -->\n## Harm\n\n| a | b |\n|---|---|\n| 1 | <script>x</script> |\n")
	write(t, dir, "markdown/sheets/sheets.md", "---\ntitle: Sheets\nsource: Test Sheets 1.0.pdf\n---\n\n# Sheets\n\n## Hunter (sheet pages 1-2)\n\nText.\n")
	write(t, dir, "markdown/stories/adv.md", "---\ntitle: Secret Adventure\nsource: Zine.pdf\n---\n\n<!-- p.1 -->\n# Twist\n")
	write(t, dir, "condensed/00_README.md", "# ignore me\n")
	write(t, dir, "condensed/rules.md", "# Summary (p. 1)\n")
	snap := &gamedata.Snapshot{ID: "test", Dir: dir}
	snap.Manifest.Text = []gamedata.TextSource{
		{Path: "condensed", Audience: "party", Role: "rules_condensed"},
		{Path: "markdown/rules", Audience: "party", Role: "rules_full"},
		{Path: "markdown/sheets", Audience: "party", Role: "rules_full"},
		{Path: "markdown/stories", Audience: "seer", Role: "adventures"},
	}
	snap.Manifest.Citations = map[string]string{"p.": "Test Rulebook 1.0", "Sheet p.": "Test Sheets 1.0"}
	return snap
}

func TestBuild(t *testing.T) {
	lib, err := Build(testSnapshot(t))
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, tab := range lib.Tabs {
		keys = append(keys, tab.Key)
	}
	if got := strings.Join(keys, ","); got != "rules,sheets,summary,adventures" {
		t.Fatalf("tabs = %s", got)
	}
	if len(lib.Visible(false)) != 3 || len(lib.Visible(true)) != 4 {
		t.Fatalf("adventures must be Seer-only")
	}
	if len(lib.Tab("summary").Docs) != 1 {
		t.Fatalf("README files should be skipped")
	}

	core := lib.Page("p", 2)
	if core == nil || core.Title != "Core Rules" || core.URL() != "/rules/rules/01-core" {
		t.Fatalf("p. 2 → %+v", core)
	}
	h := string(core.HTML)
	for _, want := range []string{`id="p-1"`, `id="p-2"`, `class="table table-sm table-bordered"`, `id="harm"`} {
		if !strings.Contains(h, want) {
			t.Errorf("rendered doc lacks %s:\n%s", want, h)
		}
	}
	if strings.Contains(h, "<script>") || strings.Contains(h, "HIPAGEMARK") {
		t.Errorf("raw HTML or placeholder leaked:\n%s", h)
	}
	if core.HeadingID("Harm") != "harm" || len(core.TOC) != 2 {
		t.Errorf("TOC = %+v", core.TOC)
	}

	sheet := lib.Page("sheet", 2)
	if sheet == nil || !strings.Contains(string(sheet.HTML), `id="sheet-1"`) {
		t.Fatalf("sheet pages from headings: %+v", sheet)
	}
	if lib.Page("p", 99) != nil {
		t.Errorf("unknown page should be nil")
	}
	// The adventure's own p. 1 must not stand in for the rulebook's.
	if lib.Page("p", 1) != core {
		t.Errorf("p. 1 should be the rulebook's")
	}
}

func TestBookKey(t *testing.T) {
	for in, want := range map[string]string{"p.": "p", "Sheet p.": "sheet", "Ref p.": "ref"} {
		if got := BookKey(in); got != want {
			t.Errorf("BookKey(%q) = %q", in, got)
		}
	}
}

// TestRealRules builds the real rules repo when HI_RULES_DIR points at a checkout.
func TestRealRules(t *testing.T) {
	dir := os.Getenv("HI_RULES_DIR")
	if dir == "" {
		t.Skip("HI_RULES_DIR not set")
	}
	snap, err := gamedata.Load(dir, "local-test")
	if err != nil {
		t.Fatal(err)
	}
	lib, err := Build(snap)
	if err != nil {
		t.Fatal(err)
	}
	for _, tab := range lib.Tabs {
		t.Logf("tab %s (%s, seer=%v): %d docs", tab.Key, tab.Label, tab.SeerOnly, len(tab.Docs))
	}
	for book, pages := range lib.pages {
		t.Logf("book %s: %d pages", book, len(pages))
	}
	for _, c := range []struct {
		book string
		page int
	}{{"p", 15}, {"p", 40}, {"p", 101}, {"p", 186}, {"sheet", 3}, {"ref", 8}} {
		if d := lib.Page(c.book, c.page); d == nil {
			t.Errorf("%s %d not found", c.book, c.page)
		} else {
			t.Logf("%s %d → %s", c.book, c.page, d.URL())
		}
	}
}
