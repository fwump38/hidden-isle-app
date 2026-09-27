// Package rules is full-text search over the synced rules text: the condensed and full editions
// and (Seer only) the adventures, split into sections by heading, each with its page cite.
package rules

import (
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"unicode"

	"gorm.io/gorm"

	"github.com/fwump38/hidden-isle-app/internal/gamedata"
)

// Hit is one search result.
type Hit struct {
	Ref      string `json:"ref"` // pass to Read for the whole section
	Path     string `json:"path"`
	Heading  string `json:"heading"`
	Page     string `json:"page,omitempty"`
	Snippet  string `json:"snippet"`
	SeerOnly bool   `json:"seer_only,omitempty"`
}

// Section is one heading's text.
type Section struct {
	Ref      string `json:"ref"`
	Path     string `json:"path"`
	Heading  string `json:"heading"`
	Page     string `json:"page,omitempty"`
	Text     string `json:"text"`
	SeerOnly bool   `json:"seer_only,omitempty"`
}

type Index struct {
	db       *gorm.DB
	mu       sync.RWMutex
	snapshot string
}

// New prepares the search tables in the app database.
func New(g *gorm.DB) (*Index, error) {
	stmts := []string{
		`CREATE VIRTUAL TABLE IF NOT EXISTS rules_fts USING fts5(ref UNINDEXED, path UNINDEXED, audience UNINDEXED, page UNINDEXED, heading, body, tokenize='porter unicode61')`,
		`CREATE TABLE IF NOT EXISTS rules_index_meta (id INTEGER PRIMARY KEY CHECK (id = 1), snapshot TEXT NOT NULL)`,
	}
	for _, q := range stmts {
		if err := g.Exec(q).Error; err != nil {
			return nil, fmt.Errorf("rules index: %w", err)
		}
	}
	ix := &Index{db: g}
	g.Raw(`SELECT snapshot FROM rules_index_meta WHERE id = 1`).Scan(&ix.snapshot)
	return ix, nil
}

// Build indexes a snapshot's text sources, unless it's already the indexed one.
func (ix *Index) Build(snap *gamedata.Snapshot) error {
	if snap == nil {
		return nil
	}
	ix.mu.Lock()
	defer ix.mu.Unlock()
	if ix.snapshot == snap.ID {
		return nil
	}
	var chunks []chunk
	for _, src := range snap.Manifest.Text {
		root := filepath.Join(snap.Dir, filepath.FromSlash(src.Path))
		err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(p, ".md") {
				return err
			}
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(snap.Dir, p)
			rel = filepath.ToSlash(rel)
			chunks = append(chunks, split(rel, snap.Audience(rel), string(b))...)
			return nil
		})
		if err != nil {
			return fmt.Errorf("index %s: %w", src.Path, err)
		}
	}
	err := ix.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(`DELETE FROM rules_fts`).Error; err != nil {
			return err
		}
		for _, c := range chunks {
			if err := tx.Exec(`INSERT INTO rules_fts (ref, path, audience, page, heading, body) VALUES (?, ?, ?, ?, ?, ?)`,
				c.ref, c.path, c.audience, c.page, c.heading, c.body).Error; err != nil {
				return err
			}
		}
		return tx.Exec(`INSERT INTO rules_index_meta (id, snapshot) VALUES (1, ?) ON CONFLICT(id) DO UPDATE SET snapshot = excluded.snapshot`, snap.ID).Error
	})
	if err != nil {
		return err
	}
	ix.snapshot = snap.ID
	slog.Info("rules indexed", "snapshot", snap.Label(), "sections", len(chunks))
	return nil
}

var wordRe = regexp.MustCompile(`[\p{L}\p{N}']+`)

// Search finds sections matching every word of query (prefix match), best first. Seer-only
// sources are included only when allowSeer is true.
func (ix *Index) Search(query string, allowSeer bool, limit int) ([]Hit, error) {
	var terms []string
	for _, w := range wordRe.FindAllString(query, -1) {
		w = strings.Trim(strings.ToLower(w), "'")
		if w != "" {
			terms = append(terms, `"`+strings.ReplaceAll(w, `"`, "")+`"*`)
		}
	}
	if len(terms) == 0 {
		return nil, fmt.Errorf("search for some words")
	}
	if limit <= 0 || limit > 30 {
		limit = 10
	}
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	q := `SELECT ref, path, heading, page, audience, snippet(rules_fts, 5, '**', '**', '…', 24) AS snip
		FROM rules_fts WHERE rules_fts MATCH ?`
	args := []any{strings.Join(terms, " AND ")}
	if !allowSeer {
		q += ` AND audience <> 'seer'`
	}
	q += ` ORDER BY bm25(rules_fts, 0, 0, 0, 0, 4.0, 1.0) LIMIT ?`
	args = append(args, limit)
	rows, err := ix.db.Raw(q, args...).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Hit
	for rows.Next() {
		var h Hit
		var aud string
		if err := rows.Scan(&h.Ref, &h.Path, &h.Heading, &h.Page, &aud, &h.Snippet); err != nil {
			return nil, err
		}
		h.SeerOnly = aud == "seer"
		out = append(out, h)
	}
	return out, rows.Err()
}

// Read returns one section by its ref, or every section of a file when ref is a file path.
func (ix *Index) Read(ref string, allowSeer bool) ([]Section, error) {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	q := `SELECT ref, path, heading, page, audience, body FROM rules_fts WHERE ref = ?`
	if !strings.Contains(ref, "#") {
		q = `SELECT ref, path, heading, page, audience, body FROM rules_fts WHERE path = ? ORDER BY rowid`
	}
	rows, err := ix.db.Raw(q, ref).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Section
	for rows.Next() {
		var s Section
		var aud string
		if err := rows.Scan(&s.Ref, &s.Path, &s.Heading, &s.Page, &aud, &s.Text); err != nil {
			return nil, err
		}
		if aud == "seer" && !allowSeer {
			continue
		}
		s.SeerOnly = aud == "seer"
		out = append(out, s)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no rules section %q (use search_rules to find refs)", ref)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------- splitting

type chunk struct {
	ref, path, audience, page, heading, body string
}

var (
	pageMarker = regexp.MustCompile(`<!--\s*p\.(\d+)\s*-->`)
	// A heading's trailing "(p. 23)", "(pp. 44-45; Sheet pp. 3-4)", "(p. 24, p. 66)"…
	headingPages = regexp.MustCompile(`\(((?:pp?|Sheet pp?|Ref pp?)\.\s*[^()]*\d[^()]*)\)\s*$`)
	headingLine  = regexp.MustCompile(`^(#{1,4})\s+(.*)$`)
)

// split cuts a markdown file into sections at headings. The page comes from the heading's own
// cite ("(pp. 44-45)", as in condensed/) or from the <!-- p.N --> markers (full text).
func split(path, audience, text string) []chunk {
	lines := strings.Split(text, "\n")
	if len(lines) > 0 && strings.TrimSpace(lines[0]) == "---" { // YAML front matter
		for i := 1; i < len(lines); i++ {
			if strings.TrimSpace(lines[i]) == "---" {
				lines = lines[i+1:]
				break
			}
		}
	}
	var out []chunk
	var stack []string // heading per level
	var body []string
	page, first, last := "", 0, 0
	flush := func() {
		txt := strings.TrimSpace(strings.Join(body, "\n"))
		body = body[:0]
		if txt == "" && len(stack) == 0 {
			return
		}
		h := strings.Join(nonEmpty(stack), " › ")
		pg := page
		if pg == "" && first > 0 {
			pg = fmt.Sprintf("p. %d", first)
			if last > first {
				pg = fmt.Sprintf("pp. %d-%d", first, last)
			}
		}
		if txt == "" {
			return
		}
		out = append(out, chunk{ref: fmt.Sprintf("%s#%d", path, len(out)+1), path: path, audience: audience, page: pg, heading: h, body: txt})
	}
	current := 0
	for _, ln := range lines {
		if m := pageMarker.FindStringSubmatch(ln); m != nil {
			fmt.Sscan(m[1], &current)
			if first == 0 {
				first = current
			}
			last = current
			continue
		}
		if m := headingLine.FindStringSubmatch(ln); m != nil {
			flush()
			level := len(m[1])
			title := strings.TrimSpace(m[2])
			for len(stack) < level {
				stack = append(stack, "")
			}
			stack = append(stack[:level-1], title)
			page = ""
			if pm := headingPages.FindStringSubmatch(title); pm != nil {
				page = strings.TrimSpace(pm[1])
			}
			first, last = current, current
			continue
		}
		body = append(body, ln)
	}
	flush()
	return out
}

func nonEmpty(ss []string) []string {
	var out []string
	for _, s := range ss {
		if strings.TrimFunc(s, unicode.IsSpace) != "" {
			out = append(out, s)
		}
	}
	return out
}
