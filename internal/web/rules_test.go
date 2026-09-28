package web

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fwump38/hidden-isle-app/internal/gamedata"
)

// withRulesText gives the fixture snapshot a small rules repo on disk.
func withRulesText(t *testing.T, srv *Server) {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"markdown/rules/02_core.md":  "---\ntitle: Game Rules: Core\nsource: Test Rulebook.pdf\npages: 14-15\n---\n\n<!-- p.14 -->\n# Challenges\n\nDraw cards.\n\n<!-- p.15 -->\n## Danger\n\nMore cards (see p. 14).\n",
		"markdown/sheets/sheets.md":  "---\ntitle: Sheets\nsource: Test Sheets.pdf\n---\n\n# Sheets\n\n## Hunter (sheet pages 1-2)\n\nWolf.\n",
		"markdown/stories/secret.md": "---\ntitle: SECRET-ADVENTURE\nsource: Zine.pdf\n---\n\n# Twist\n\nThe butler did it.\n",
	}
	for rel, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	snap := srv.Data.Current()
	snap.Dir = dir
	snap.Manifest.Text = []gamedata.TextSource{
		{Path: "markdown/rules", Audience: "party", Role: "rules_full"},
		{Path: "markdown/sheets", Audience: "party", Role: "rules_full"},
		{Path: "markdown/stories", Audience: "seer", Role: "adventures"},
	}
	snap.Manifest.Citations = map[string]string{"p.": "Test Rulebook", "Sheet p.": "Test Sheets"}
}

func TestRuleBrowser(t *testing.T) {
	st, _, srv := newSiteWithServer(t)
	withRulesText(t, srv)

	code, body := st.get("Ana", "/rules")
	if code != http.StatusOK || !strings.Contains(body, "Challenges") || !strings.Contains(body, `href="/rules/sheets"`) {
		t.Fatalf("/rules: %d\n%s", code, body)
	}
	if strings.Contains(body, "SECRET-ADVENTURE") || strings.Contains(body, `href="/rules/adventures"`) {
		t.Fatal("players must not see the adventures tab")
	}
	if !strings.Contains(body, `data-cites="`) {
		t.Fatal("layout should carry the cite prefixes for app.js")
	}

	code, body = st.get("Ana", "/rules/page/p/15")
	if code != http.StatusOK || !strings.Contains(body, `data-scroll-to="p-15"`) || !strings.Contains(body, `id="p-15"`) {
		t.Fatalf("page link: %d\n%s", code, body)
	}
	code, body = st.get("Ana", "/rules/page/sheet/2")
	if code != http.StatusOK || !strings.Contains(body, `data-scroll-to="sheet-2"`) || !strings.Contains(body, "Wolf.") {
		t.Fatalf("sheet page link: %d\n%s", code, body)
	}
	code, body = st.get("Ana", "/rules/page/p/999")
	if code != http.StatusNotFound || !strings.Contains(body, "p. 999 isn") {
		t.Fatalf("missing page: %d", code)
	}

	if code, _ := st.get("Ana", "/rules/adventures/secret"); code != http.StatusNotFound {
		t.Fatalf("player opened a Seer-only doc: %d", code)
	}
	code, body = st.get("Seer", "/rules/adventures/secret")
	if code != http.StatusOK || !strings.Contains(body, "The butler did it.") {
		t.Fatalf("Seer adventure: %d", code)
	}
	code, body = st.get("Ana", "/rules/rules/02-core?at=danger")
	if code != http.StatusOK || !strings.Contains(body, `data-scroll-to="danger"`) {
		t.Fatalf("anchor: %d", code)
	}
	if _, body = st.get("Ana", "/rules/rules/02-core?at=%22%3E%3Cscript%3E"); strings.Contains(body, "data-scroll-to") {
		t.Fatal("a bad anchor must be dropped")
	}
}

func TestHelpIconCarriesText(t *testing.T) {
	st, _, srv := newSiteWithServer(t)
	withRulesText(t, srv)
	st.post("Seer", "/campaigns", map[string][]string{"name": {"C"}, "mode": {"group"}})
	_, body := st.get("Seer", "/c/1/settings")
	if !strings.Contains(body, `data-hi-help="Optional rule (pp. 93-94)`) {
		t.Fatalf("help icon text missing:\n%s", body)
	}
}
