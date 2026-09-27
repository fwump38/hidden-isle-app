package rules

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fwump38/hidden-isle-app/internal/db"
	"github.com/fwump38/hidden-isle-app/internal/gamedata"
)

func TestSplit(t *testing.T) {
	condensed := "---\ntitle: x\n---\n# Core Rules\nIntro.\n## Harm (p. 23)\nEach suit has 2 harm boxes.\n### Trauma (p. 24, p. 66)\nThe worst harm.\n"
	cs := split("condensed/rules/01.md", "party", condensed)
	if len(cs) != 3 {
		t.Fatalf("chunks = %d: %+v", len(cs), cs)
	}
	if cs[1].heading != "Core Rules › Harm (p. 23)" || cs[1].page != "p. 23" {
		t.Errorf("chunk 2 = %+v", cs[1])
	}
	if cs[2].page != "p. 24, p. 66" {
		t.Errorf("chunk 3 page = %q", cs[2].page)
	}
	full := "# Chapter\n<!-- p.44 -->\n## The Occultist\nText on 44.\n<!-- p.45 -->\nMore on 45.\n## Next\n<!-- p.46 -->\nx\n"
	cs = split("markdown/rules/04.md", "party", full)
	if cs[0].heading != "Chapter › The Occultist" || cs[0].page != "pp. 44-45" || strings.Contains(cs[0].body, "<!--") {
		t.Errorf("full-text chunk = %+v", cs[0])
	}
}

func TestIndexWithRealRules(t *testing.T) {
	src := os.Getenv("HI_TEST_GAMEDATA")
	if src == "" {
		t.Skip("HI_TEST_GAMEDATA not set")
	}
	g, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	snap, err := gamedata.Load(src, "test")
	if err != nil {
		t.Fatal(err)
	}
	ix, err := New(g)
	if err != nil {
		t.Fatal(err)
	}
	if err := ix.Build(snap); err != nil {
		t.Fatal(err)
	}
	hits, err := ix.Search("harm suit fewer card", false, 5)
	if err != nil || len(hits) == 0 {
		t.Fatalf("search: %v %v", hits, err)
	}
	if hits[0].Page == "" {
		t.Errorf("first hit has no page: %+v", hits[0])
	}
	// Adventures are Seer-only.
	seer, _ := ix.Search("Metaphagus", true, 10)
	player, _ := ix.Search("Metaphagus", false, 10)
	for _, h := range player {
		if h.SeerOnly || strings.HasPrefix(h.Path, "markdown/stories/") {
			t.Errorf("player search returned an adventure: %s", h.Path)
		}
	}
	found := false
	for _, h := range seer {
		found = found || strings.HasPrefix(h.Path, "markdown/stories/")
	}
	if !found {
		t.Error("Seer search should find the adventure")
	}
	secs, err := ix.Read(hits[0].Ref, false)
	if err != nil || len(secs) != 1 || secs[0].Text == "" {
		t.Errorf("read: %v %v", secs, err)
	}
	if _, err := ix.Read("markdown/stories/fhyp_03_the_metaphagus.md", false); err == nil {
		t.Error("player read an adventure file")
	}
}
