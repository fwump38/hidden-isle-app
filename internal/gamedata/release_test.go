package gamedata

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// makeReleases builds a git repo with releases v1.0.0, v1.1.0 and v2.0.0 of the real rules data.
func makeReleases(t *testing.T) string {
	t.Helper()
	src := rulesRepo(t)
	dir := t.TempDir()
	var m Manifest
	if err := readYAML(filepath.Join(src, manifestFile), &m); err != nil {
		t.Fatal(err)
	}
	for _, rel := range m.paths() {
		if err := copyPath(filepath.Join(src, rel), filepath.Join(dir, rel)); err != nil {
			t.Fatal(err)
		}
	}
	repo, err := git.PlainInit(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	wt, _ := repo.Worktree()
	manifest, _ := os.ReadFile(filepath.Join(dir, manifestFile))
	versionLine := regexp.MustCompile(`(?m)^version: .*$`)
	sig := &object.Signature{Name: "t", Email: "t@example.com", When: time.Now()}
	for _, v := range []string{"1.0.0", "1.1.0", "2.0.0"} {
		out := versionLine.ReplaceAll(manifest, []byte("version: "+v))
		if err := os.WriteFile(filepath.Join(dir, manifestFile), out, 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := wt.Add("."); err != nil {
			t.Fatal(err)
		}
		h, err := wt.Commit("release "+v, &git.CommitOptions{Author: sig})
		if err != nil {
			t.Fatal(err)
		}
		tag := "v" + v
		if v == "1.0.0" {
			tag = v // tags may be spelled without the v
		}
		if _, err := repo.CreateTag(tag, h, nil); err != nil {
			t.Fatal(err)
		}
	}
	return "file://" + dir
}

func TestReleasePinning(t *testing.T) {
	url := makeReleases(t)
	data := t.TempDir()
	ctx := context.Background()

	s := NewStore(data, url, "release", "")
	if err := s.Sync(ctx); err != nil {
		t.Fatalf("first sync: %v", err)
	}
	st := s.Status()
	if st.Current != "v1.1.0" || st.Version != "1.1.0" {
		t.Fatalf("first run should install the newest compatible release, got %s (%s)", st.Current, st.Version)
	}
	if len(st.Updates) != 0 || !slices.Equal(st.TooNew, []string{"v2.0.0"}) {
		t.Errorf("updates %v, too new %v", st.Updates, st.TooNew)
	}
	if err := s.Install(ctx, "v2.0.0"); err == nil {
		t.Error("installed a release with an unsupported major version")
	}
	if err := s.Install(ctx, "v9.9.9"); err == nil {
		t.Error("installed a release that doesn't exist")
	}
	if err := s.Install(ctx, "1.0.0"); err != nil {
		t.Fatalf("install 1.0.0: %v", err)
	}
	if st := s.Status(); st.Current != "1.0.0" || !slices.Equal(st.Updates, []string{"v1.1.0"}) {
		t.Errorf("after pinning v1.0.0: current %s, updates %v", st.Current, st.Updates)
	}

	// A restart keeps the pin rather than jumping to the newest release.
	s2 := NewStore(data, url, "release", "")
	if err := s2.LoadExisting(); err != nil {
		t.Fatal(err)
	}
	if err := s2.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if got := s2.Current().Label(); got != "v1.0.0" {
		t.Errorf("after restart: %s, want the pinned v1.0.0", got)
	}

	// A fixed tag in GAMEDATA_REF wins; a branch follows commits.
	s3 := NewStore(t.TempDir(), url, "v1.1.0", "")
	if err := s3.Sync(ctx); err != nil || s3.Current().ID != "v1.1.0" {
		t.Errorf("fixed tag: %v %v", err, s3.Current())
	}
	if err := s3.Install(ctx, "1.0.0"); err == nil {
		t.Error("Install should refuse when GAMEDATA_REF pins a tag")
	}
}
