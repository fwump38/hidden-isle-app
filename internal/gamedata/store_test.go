package gamedata

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// These tests need a checkout of the rules repo: HI_TEST_GAMEDATA=/path/to/The-Hidden-Isle go test ./...
func rulesRepo(t *testing.T) string {
	t.Helper()
	dir := os.Getenv("HI_TEST_GAMEDATA")
	if dir == "" {
		t.Skip("HI_TEST_GAMEDATA not set")
	}
	return dir
}

func TestSyncLocalAndKeepLastGood(t *testing.T) {
	src := rulesRepo(t)
	work := t.TempDir()
	// Copy the manifest-listed paths so the test can break the source afterwards.
	var m Manifest
	if err := readYAML(filepath.Join(src, manifestFile), &m); err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(work, "repo")
	for _, rel := range m.paths() {
		if err := copyPath(filepath.Join(src, rel), filepath.Join(repo, rel)); err != nil {
			t.Fatal(err)
		}
	}
	s := NewStore(filepath.Join(work, "data"), repo, "main", "")
	if err := s.Sync(context.Background()); err != nil {
		t.Fatalf("first sync: %v", err)
	}
	good := s.Current()
	if good == nil || good.Class("hunter") == nil || len(good.Cards.Vision) != 38 {
		t.Fatalf("snapshot not loaded correctly: %+v", good)
	}
	if len(m.Data.Homebrew) > 0 && (good.Homebrew.Names == nil || good.HomebrewRegion("Dioscoria") == nil) {
		t.Fatalf("manifest lists homebrew %v but it didn't load", m.Data.Homebrew)
	}
	if got := good.Audience("markdown/stories/fhyp_01_a_muse_of_fire.md"); got != "seer" {
		t.Errorf("stories audience = %q, want seer", got)
	}
	if got := good.Audience("condensed/rules/01_core_rules.md"); got != "party" {
		t.Errorf("condensed audience = %q, want party", got)
	}

	// Break the source: the sync must fail and keep the good snapshot.
	if err := os.WriteFile(filepath.Join(repo, "data/generated/cards.yaml"), []byte("vision: []\npips: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := s.Sync(context.Background()); err == nil {
		t.Fatal("sync of broken data succeeded")
	}
	if s.Current() != good {
		t.Fatal("broken sync replaced the good snapshot")
	}
	if s.Status().LastError == "" {
		t.Error("status should report the error")
	}

	// A restart loads the recorded snapshot without the source.
	s2 := NewStore(filepath.Join(work, "data"), "", "main", "")
	if err := s2.LoadExisting(); err != nil {
		t.Fatalf("LoadExisting: %v", err)
	}
	if s2.Current().ID != good.ID {
		t.Fatalf("restart loaded %s, want %s", s2.Current().ID, good.ID)
	}
}

func TestWithinRejectsEscapes(t *testing.T) {
	for _, bad := range []string{"../etc/passwd", "/etc/passwd", "data/../../x"} {
		if _, err := within("/tmp/x", bad); err == nil {
			t.Errorf("within accepted %q", bad)
		}
	}
}
