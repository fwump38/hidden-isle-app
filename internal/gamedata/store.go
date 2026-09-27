package gamedata

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-git/go-git/v5"
	gitconfig "github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/go-git/go-git/v5/plumbing/transport/http"
	"golang.org/x/mod/semver"
)

const keepSnapshots = 3

// Store keeps the current snapshot and syncs new ones from the source repo.
type Store struct {
	root   string // <data dir>/gamedata
	source string // git URL (https or ssh) or local directory
	ref    string
	token  string // HTTPS token (e.g. a GitHub fine-grained PAT)

	keyPath    string // SSH deploy key, created on first use
	knownHosts string // known_hosts for non-GitHub SSH hosts

	cur atomic.Pointer[Snapshot]
	mu  sync.Mutex // one sync at a time

	statusMu sync.Mutex
	status   Status
}

// Status is shown on the Seer's admin page.
type Status struct {
	Source      string
	Ref         string
	Mode        string // release | tag | branch | local
	Current     string
	Version     string // manifest version of the snapshot in use
	LoadedAt    time.Time
	LastAttempt time.Time
	LastError   string
	Releases    []string // every release tag, newest first
	Updates     []string // newer releases this app can install, newest first
	TooNew      []string // newer releases that need a newer app (other major version)
}

// SupportedMajors are the rules-data major versions this build can read.
var SupportedMajors = []string{"v1"}

// semverOf normalizes a release tag ("1.2.0" or "v1.2.0") to "v1.2.0"; "" if it isn't one.
func semverOf(tag string) string {
	v := tag
	if !strings.HasPrefix(v, "v") {
		v = "v" + v
	}
	if !semver.IsValid(v) || semver.Prerelease(v) != "" {
		return ""
	}
	return v
}

func supported(tag string) bool {
	v := semverOf(tag)
	return v != "" && slices.Contains(SupportedMajors, semver.Major(v))
}

// Mode reports how the store picks what to load.
func (s *Store) Mode() string {
	switch {
	case s.source != "" && isLocalDir(s.source):
		return "local"
	case s.ref == "" || s.ref == "release":
		return "release"
	case semverOf(s.ref) != "":
		return "tag"
	}
	return "branch"
}

func isLocalDir(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

func NewStore(dataDir, source, ref, token string) *Store {
	return &Store{root: filepath.Join(dataDir, "gamedata"), source: source, ref: ref, token: token,
		keyPath: filepath.Join(dataDir, "secrets", "gamedata_ed25519"),
		status:  Status{Source: redact(source), Ref: ref}}
}

// SetKnownHosts sets a known_hosts file for SSH hosts other than github.com.
func (s *Store) SetKnownHosts(path string) { s.knownHosts = path }

// UsesSSH reports whether the source is an SSH URL (and so needs the deploy key).
func (s *Store) UsesSSH() bool { return IsSSHURL(s.source) }

// DeployPublicKey returns the public deploy key to add to the rules repo ("" unless SSH is used).
func (s *Store) DeployPublicKey() (string, error) {
	if !s.UsesSSH() {
		return "", nil
	}
	_, pub, err := DeployKey(s.keyPath)
	return pub, err
}

// authMethod returns credentials for the source, or nil for anonymous HTTPS.
func (s *Store) authMethod() (transport.AuthMethod, error) {
	if s.UsesSSH() {
		return sshAuth(s.source, s.keyPath, s.knownHosts)
	}
	if s.token != "" {
		return &http.BasicAuth{Username: "x-access-token", Password: s.token}, nil
	}
	return nil, nil // a nil interface, not a typed nil pointer
}

// Current returns the snapshot in use, or nil before the first successful load.
func (s *Store) Current() *Snapshot { return s.cur.Load() }

func (s *Store) Status() Status {
	s.statusMu.Lock()
	defer s.statusMu.Unlock()
	st := s.status
	st.Mode = s.Mode()
	if c := s.Current(); c != nil {
		st.Current, st.LoadedAt, st.Version = c.ID, c.LoadedAt, c.Manifest.Version
	}
	st.Updates, st.TooNew = nil, nil
	cur := "v" + st.Version
	for _, t := range st.Releases {
		if semver.Compare(semverOf(t), cur) <= 0 {
			continue
		}
		if supported(t) {
			st.Updates = append(st.Updates, t)
		} else {
			st.TooNew = append(st.TooNew, t)
		}
	}
	return st
}

// Install switches to a release (from the admin page) and pins it once it loads.
func (s *Store) Install(ctx context.Context, tag string) error {
	if s.Mode() != "release" {
		return fmt.Errorf("installing releases needs GAMEDATA_REF=release (it's %q)", s.ref)
	}
	if !supported(tag) {
		return fmt.Errorf("this app can't read %s; it supports %v", tag, SupportedMajors)
	}
	return s.sync(ctx, tag)
}

// LoadExisting loads the snapshot recorded in <root>/current, so the app starts without network.
func (s *Store) LoadExisting() error {
	b, err := os.ReadFile(filepath.Join(s.root, "current"))
	if err != nil {
		return err
	}
	id := strings.TrimSpace(string(b))
	snap, err := Load(filepath.Join(s.root, "snapshots", id), id)
	if err != nil {
		return err
	}
	s.cur.Store(snap)
	return nil
}

// Run syncs now and then every interval until ctx ends.
func (s *Store) Run(ctx context.Context, every time.Duration) {
	for {
		if err := s.Sync(ctx); err != nil && ctx.Err() == nil {
			slog.Error("game data sync failed; keeping last good snapshot", "err", err)
		}
		if every <= 0 {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(every):
		}
	}
}

// Sync fetches the source, and if what should be loaded changed, copies, validates and swaps in
// a new snapshot. In release mode it keeps the pinned release (or, on first run, installs the
// newest compatible one) and only lists newer releases. On failure the current snapshot stays.
func (s *Store) Sync(ctx context.Context) error { return s.sync(ctx, "") }

func (s *Store) sync(ctx context.Context, want string) (err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	defer func() {
		s.statusMu.Lock()
		s.status.LastAttempt = time.Now()
		s.status.LastError = ""
		if err != nil {
			s.status.LastError = err.Error()
		}
		s.statusMu.Unlock()
	}()
	if s.source == "" {
		return errors.New("GAMEDATA_REPO is not set")
	}
	tree, id, err := s.checkout(ctx, want)
	if err != nil {
		return err
	}
	if cur := s.Current(); cur != nil && cur.ID == id {
		return nil
	}
	var m Manifest
	if err := readYAML(filepath.Join(tree, manifestFile), &m); err != nil {
		return fmt.Errorf("read manifest: %w", err)
	}
	snapsDir := filepath.Join(s.root, "snapshots")
	tmp := filepath.Join(snapsDir, id+".tmp")
	_ = os.RemoveAll(tmp)
	for _, rel := range m.paths() {
		src, err := within(tree, rel)
		if err != nil {
			return err
		}
		if err := copyPath(src, filepath.Join(tmp, filepath.FromSlash(rel))); err != nil {
			_ = os.RemoveAll(tmp)
			return fmt.Errorf("copy %s: %w", rel, err)
		}
	}
	snap, err := Load(tmp, id)
	if err != nil {
		_ = os.RemoveAll(tmp)
		return fmt.Errorf("snapshot %s failed validation: %w", short(id), err)
	}
	final := filepath.Join(snapsDir, id)
	_ = os.RemoveAll(final)
	if err := os.Rename(tmp, final); err != nil {
		return err
	}
	snap.Dir = final
	if err := writeAtomic(filepath.Join(s.root, "current"), []byte(id+"\n")); err != nil {
		return err
	}
	s.cur.Store(snap)
	if s.Mode() == "release" {
		if err := writeAtomic(filepath.Join(s.root, "pin"), []byte(id+"\n")); err != nil {
			return err
		}
	}
	slog.Info("game data loaded", "snapshot", snap.Label(), "classes", len(snap.Classes.Classes))
	s.prune(id)
	return nil
}

// checkout returns a directory holding the source tree and an id for its content: the release
// tag in release/tag mode, the commit in branch mode, a content hash for a local directory.
func (s *Store) checkout(ctx context.Context, want string) (dir, id string, err error) {
	mode := s.Mode()
	if mode == "local" {
		id, err := hashTree(s.source)
		return s.source, "local-" + id, err
	}
	clone := filepath.Join(s.root, "repo")
	auth, err := s.authMethod()
	if err != nil {
		return "", "", err
	}
	repo, err := git.PlainOpen(clone)
	if err == nil {
		// GAMEDATA_REPO changed (e.g. HTTPS to SSH): start over with a fresh clone.
		if rem, rErr := repo.Remote("origin"); rErr != nil || len(rem.Config().URLs) == 0 || rem.Config().URLs[0] != s.source {
			_ = os.RemoveAll(clone)
			repo, err = nil, git.ErrRepositoryNotExists
		}
	}
	if errors.Is(err, git.ErrRepositoryNotExists) {
		repo, err = git.PlainCloneContext(ctx, clone, false, &git.CloneOptions{URL: s.source, Auth: auth, Tags: git.AllTags})
		if err != nil {
			_ = os.RemoveAll(clone)
			return "", "", fmt.Errorf("clone %s: %w", redact(s.source), err)
		}
	} else if err != nil {
		return "", "", err
	}

	var target plumbing.Hash
	switch mode {
	case "branch":
		branch := plumbing.NewBranchReferenceName(s.ref)
		remoteRef := plumbing.NewRemoteReferenceName("origin", s.ref)
		err = repo.FetchContext(ctx, &git.FetchOptions{Auth: auth, Force: true,
			RefSpecs: []gitconfig.RefSpec{gitconfig.RefSpec(fmt.Sprintf("+%s:%s", branch, remoteRef))}})
		if err != nil && !errors.Is(err, git.NoErrAlreadyUpToDate) {
			return "", "", fmt.Errorf("fetch: %w", err)
		}
		ref, err := repo.Reference(remoteRef, true)
		if err != nil {
			return "", "", fmt.Errorf("branch %s: %w", s.ref, err)
		}
		target, id = ref.Hash(), ref.Hash().String()
	default: // release or tag
		err = repo.FetchContext(ctx, &git.FetchOptions{Auth: auth, Force: true, Tags: git.AllTags,
			RefSpecs: []gitconfig.RefSpec{"+refs/tags/*:refs/tags/*"}})
		if err != nil && !errors.Is(err, git.NoErrAlreadyUpToDate) {
			return "", "", fmt.Errorf("fetch: %w", err)
		}
		releases, err := releaseTags(repo)
		if err != nil {
			return "", "", err
		}
		s.statusMu.Lock()
		s.status.Releases = releases
		s.statusMu.Unlock()
		id = want
		if id == "" && mode == "tag" {
			id = s.ref
		}
		if id == "" {
			id = s.pinned()
		}
		if id == "" { // first run: the newest release this app supports
			for _, t := range releases {
				if supported(t) {
					id = t
					break
				}
			}
		}
		if id == "" {
			return "", "", errors.New("the rules repo has no releases yet: push a tag like v1.0.0 (see its CLAUDE.md)")
		}
		if !slices.Contains(releases, id) {
			return "", "", fmt.Errorf("release %s doesn't exist in the rules repo", id)
		}
		h, err := repo.ResolveRevision(plumbing.Revision("refs/tags/" + id))
		if err != nil {
			return "", "", fmt.Errorf("release %s: %w", id, err)
		}
		target = *h
	}
	if cur := s.Current(); cur != nil && cur.ID == id {
		return clone, id, nil // nothing to check out
	}
	wt, err := repo.Worktree()
	if err != nil {
		return "", "", err
	}
	if err := wt.Reset(&git.ResetOptions{Commit: target, Mode: git.HardReset}); err != nil {
		return "", "", fmt.Errorf("checkout %s: %w", short(id), err)
	}
	return clone, id, nil
}

// pinned returns the release recorded by the last successful install, if any.
func (s *Store) pinned() string {
	b, err := os.ReadFile(filepath.Join(s.root, "pin"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// releaseTags lists the repo's semver tags, newest first.
func releaseTags(repo *git.Repository) ([]string, error) {
	iter, err := repo.Tags()
	if err != nil {
		return nil, err
	}
	var out []string
	_ = iter.ForEach(func(r *plumbing.Reference) error {
		if name := r.Name().Short(); semverOf(name) != "" {
			out = append(out, name)
		}
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return semver.Compare(semverOf(out[i]), semverOf(out[j])) > 0 })
	return out, nil
}

func (s *Store) prune(keep string) {
	dir := filepath.Join(s.root, "snapshots")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	type snap struct {
		name string
		mod  time.Time
	}
	var snaps []snap
	for _, e := range entries {
		if info, err := e.Info(); err == nil && e.IsDir() && e.Name() != keep {
			snaps = append(snaps, snap{e.Name(), info.ModTime()})
		}
	}
	sort.Slice(snaps, func(i, j int) bool { return snaps[i].mod.After(snaps[j].mod) })
	for i, sn := range snaps {
		if i >= keepSnapshots-1 || strings.HasSuffix(sn.name, ".tmp") {
			_ = os.RemoveAll(filepath.Join(dir, sn.name))
		}
	}
}

// hashTree fingerprints a local source directory (manifest-listed files only would need the
// manifest first; hashing the manifest plus data/ is enough to detect edits during development).
func hashTree(root string) (string, error) {
	h := sha256.New()
	for _, rel := range []string{manifestFile, "data", "condensed", "markdown"} {
		p := filepath.Join(root, rel)
		if _, err := os.Stat(p); err != nil {
			continue
		}
		err := filepath.WalkDir(p, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			fmt.Fprintf(h, "%s %d %d\n", path, info.Size(), info.ModTime().UnixNano())
			return nil
		})
		if err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(h.Sum(nil))[:16], nil
}

func copyPath(src, dst string) error {
	st, err := os.Stat(src)
	if err != nil {
		return err
	}
	if !st.IsDir() {
		return copyFile(src, dst)
	}
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o750)
		}
		if !d.Type().IsRegular() {
			return nil // skip symlinks and other special files
		}
		return copyFile(path, target)
	})
}

func copyFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func writeAtomic(path string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o640); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func short(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// redact hides credentials embedded in a URL.
func redact(u string) string {
	if i := strings.Index(u, "@"); i > 0 && strings.Contains(u[:i], "://") {
		return u[:strings.Index(u, "://")+3] + "***" + u[i:]
	}
	return u
}

// StaticStore returns a store that always serves snap (for tests and tools).
func StaticStore(snap *Snapshot) *Store {
	s := &Store{}
	s.cur.Store(snap)
	return s
}
