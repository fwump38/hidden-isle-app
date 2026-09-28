// Package rulebook renders the synced rules text for the in-app rule browser: one tab per text
// source in the manifest, one document per markdown file, with an anchor on every printed page
// so page cites anywhere in the app ("p. 15", "Sheet p. 3", "Ref p. 8") can link straight to it.
package rulebook

import (
	"bytes"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"unicode"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	extast "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"

	"github.com/fwump38/hidden-isle-app/internal/gamedata"
)

// Library is the browsable rules text of one snapshot. Read-only once built.
type Library struct {
	Snapshot string
	Tabs     []*Tab
	// Cites maps each cite prefix ("p.", "Sheet p.", "Ref p.") to its book key ("p", "sheet", "ref").
	Cites map[string]string

	docs  map[string]*Doc         // by "tab/doc"
	pages map[string]map[int]*Doc // book key → printed page → document holding it
}

// Tab is one text source (a folder or a single file).
type Tab struct {
	Key      string
	Label    string
	SeerOnly bool
	Docs     []*Doc
}

// Doc is one markdown file, rendered.
type Doc struct {
	Tab      *Tab
	Key      string // unique within its tab
	Path     string // repo-relative
	Title    string
	Pages    string // "pp. 13-27", from the front matter
	SeerOnly bool
	HTML     template.HTML
	TOC      []Heading
	headings map[string]string // heading text → id (first occurrence)
}

// Heading is one table-of-contents entry.
type Heading struct {
	Level int
	ID    string
	Text  string
}

// URL is the document's address in the browser.
func (d *Doc) URL() string { return "/rules/" + d.Tab.Key + "/" + d.Key }

// HeadingID returns the anchor for a heading's text, or "".
func (d *Doc) HeadingID(heading string) string { return d.headings[strings.TrimSpace(heading)] }

// Visible returns the tabs a reader may see.
func (l *Library) Visible(seer bool) []*Tab {
	var out []*Tab
	for _, t := range l.Tabs {
		if seer || !t.SeerOnly {
			out = append(out, t)
		}
	}
	return out
}

// Tab returns the tab with key, or nil.
func (l *Library) Tab(key string) *Tab {
	for _, t := range l.Tabs {
		if t.Key == key {
			return t
		}
	}
	return nil
}

// Doc returns a document by tab and doc key, or nil.
func (l *Library) Doc(tab, doc string) *Doc { return l.docs[tab+"/"+doc] }

// DocByPath returns the document for a repo-relative path, or nil.
func (l *Library) DocByPath(path string) *Doc {
	for _, d := range l.docs {
		if d.Path == path {
			return d
		}
	}
	return nil
}

// Page returns the document holding a book's printed page, or nil.
func (l *Library) Page(book string, page int) *Doc { return l.pages[book][page] }

// PageAnchor is the element id of a printed page's marker.
func PageAnchor(book string, page int) string { return fmt.Sprintf("%s-%d", book, page) }

// Books lists the book keys that have pages, e.g. for a "not found" message.
func (l *Library) Books() []string {
	var out []string
	for b := range l.pages {
		out = append(out, b)
	}
	slices.Sort(out)
	return out
}

// ---------------------------------------------------------------- cache

// Cache builds a Library per snapshot on first use and keeps the latest.
type Cache struct {
	mu  sync.Mutex
	lib *Library
}

// For returns the library for snap, building it if the snapshot changed.
func (c *Cache) For(snap *gamedata.Snapshot) *Library {
	if snap == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.lib == nil || c.lib.Snapshot != snap.ID {
		lib, err := Build(snap)
		if err != nil {
			slog.Error("rule browser", "snapshot", snap.Label(), "err", err)
		}
		c.lib = lib
	}
	return c.lib
}

// ---------------------------------------------------------------- building

// Build reads and renders every markdown file in the snapshot's text sources.
func Build(snap *gamedata.Snapshot) (*Library, error) {
	lib := &Library{Snapshot: snap.ID, Cites: map[string]string{}, docs: map[string]*Doc{}, pages: map[string]map[int]*Doc{}}
	titles := map[string]string{} // book title → book key
	for prefix, title := range snap.Manifest.Citations {
		key := BookKey(prefix)
		lib.Cites[prefix] = key
		titles[title] = key
	}
	sources := slices.Clone(snap.Manifest.Text)
	// The full text first (it holds the page markers), then errata, the condensed edition, and
	// anything else in manifest order.
	rank := map[string]int{"rules_full": 0, "errata": 1, "rules_condensed": 2}
	slices.SortStableFunc(sources, func(a, b gamedata.TextSource) int { return rankOf(rank, a.Role) - rankOf(rank, b.Role) })

	var errs []error
	for _, src := range sources {
		tab := &Tab{Label: tabLabel(src), SeerOnly: src.Audience != "party"}
		tab.Key = uniqueKey(slug(tab.Label), func(k string) bool { return lib.Tab(k) != nil || k == "page" || k == "search" })
		root := filepath.Join(snap.Dir, filepath.FromSlash(src.Path))
		err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(p, ".md") || isReadme(d.Name()) {
				return err
			}
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(snap.Dir, p)
			doc, book, pages := render(filepath.ToSlash(rel), string(b), titles)
			doc.Tab, doc.SeerOnly = tab, tab.SeerOnly
			doc.Key = uniqueKey(slug(strings.TrimSuffix(d.Name(), ".md")), func(k string) bool { return lib.docs[tab.Key+"/"+k] != nil })
			tab.Docs = append(tab.Docs, doc)
			lib.docs[tab.Key+"/"+doc.Key] = doc
			if book != "" && !doc.SeerOnly {
				if lib.pages[book] == nil {
					lib.pages[book] = map[int]*Doc{}
				}
				for _, pg := range pages {
					if lib.pages[book][pg] == nil { // first source wins
						lib.pages[book][pg] = doc
					}
				}
			}
			return nil
		})
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", src.Path, err))
		}
		if len(tab.Docs) > 0 {
			lib.Tabs = append(lib.Tabs, tab)
		}
	}
	if len(errs) > 0 {
		return lib, errs[0]
	}
	return lib, nil
}

func rankOf(rank map[string]int, role string) int {
	if r, ok := rank[role]; ok {
		return r
	}
	return len(rank)
}

// BookKey turns a cite prefix into a URL-safe book key: "p." → "p", "Sheet p." → "sheet".
func BookKey(prefix string) string {
	k := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(prefix), "p."))
	if k == "" {
		return "p"
	}
	return slug(k)
}

func tabLabel(src gamedata.TextSource) string {
	switch src.Role {
	case "rules_condensed":
		return "Summary"
	case "errata":
		return "Errata"
	case "adventures":
		return "Adventures"
	}
	return humanize(strings.TrimSuffix(filepath.Base(src.Path), ".md"))
}

func isReadme(name string) bool {
	n := strings.ToLower(name)
	return strings.HasPrefix(n, "readme") || strings.HasPrefix(n, "00_readme")
}

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

func slug(s string) string {
	s = strings.Trim(nonSlug.ReplaceAllString(strings.ToLower(s), "-"), "-")
	if s == "" {
		return "x"
	}
	return s
}

func uniqueKey(base string, taken func(string) bool) string {
	k := base
	for i := 2; taken(k); i++ {
		k = fmt.Sprintf("%s-%d", base, i)
	}
	return k
}

// humanize turns "08_city_of_dioscoria" into "City of dioscoria".
func humanize(s string) string {
	s = strings.TrimLeftFunc(s, func(r rune) bool { return unicode.IsDigit(r) || r == '_' || r == '-' })
	s = strings.ReplaceAll(strings.ReplaceAll(s, "_", " "), "-", " ")
	if s == "" {
		return "Text"
	}
	r := []rune(s)
	r[0] = unicode.ToUpper(r[0])
	return string(r)
}

// ---------------------------------------------------------------- rendering

type frontMatter struct {
	Title, Source, Pages string
}

var (
	// A page marker on its own line: <!-- p.15 --> or <!-- p.2-3 -->.
	markerLine = regexp.MustCompile(`^\s*<!--\s*p\.(\d+)(?:\s*-\s*(\d+))?\s*-->\s*$`)
	// A heading naming its character-sheet pages: "## Hunter (sheet pages 1-2)".
	sheetHeading = regexp.MustCompile(`(?i)^#{1,6}\s.*\(sheet pages?\s+(\d+)(?:\s*-\s*(\d+))?\)`)
)

var md = goldmark.New(
	goldmark.WithExtensions(extension.GFM),
	goldmark.WithParserOptions(parser.WithAutoHeadingID()),
)

// render turns one file into a Doc. It returns the book the file belongs to (from its front
// matter's source) and the printed pages it holds.
func render(rel, src string, titles map[string]string) (*Doc, string, []int) {
	fm, body := splitFrontMatter(src)
	book := ""
	for title, key := range titles {
		if fm.Source != "" && strings.HasPrefix(fm.Source, title) {
			book = key
		}
	}
	lines := strings.Split(body, "\n")
	hasMarkers := slices.ContainsFunc(lines, markerLine.MatchString)
	var pages []int
	var out []string
	for _, ln := range lines {
		if !hasMarkers {
			// Character sheets have no markers; their headings name the pages instead.
			if m := sheetHeading.FindStringSubmatch(ln); m != nil {
				out = append(out, fmt.Sprintf("<!-- p.%s -->", strings.Join(nonEmpty(m[1:]), "-")), "")
				ln = strings.TrimRight(ln, " ")
			}
		}
		out = append(out, ln)
	}
	for i, ln := range out {
		if m := markerLine.FindStringSubmatch(ln); m != nil {
			a, b := pageRange(m[1], m[2])
			for p := a; p <= b; p++ {
				pages = append(pages, p)
			}
			out[i] = "\nHIPAGEMARK:" + strings.Join(nonEmpty(m[1:]), "-") + "\n"
		}
	}
	source := []byte(strings.Join(out, "\n"))
	doc := md.Parser().Parse(text.NewReader(source))

	d := &Doc{Path: rel, Title: fm.Title, Pages: pagesLabel(fm.Pages), headings: map[string]string{}}
	ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch n := n.(type) {
		case *ast.Heading:
			id, _ := n.AttributeString("id")
			idStr := string(asBytes(id))
			t := plainText(n, source)
			if d.Title == "" && n.Level == 1 {
				d.Title = t
			}
			if n.Level <= 2 {
				d.TOC = append(d.TOC, Heading{Level: n.Level, ID: idStr, Text: t})
			}
			if _, ok := d.headings[t]; !ok {
				d.headings[t] = idStr
			}
			return ast.WalkSkipChildren, nil
		case *extast.Table:
			n.SetAttributeString("class", []byte("table table-sm table-bordered"))
		}
		return ast.WalkContinue, nil
	})
	if d.Title == "" {
		d.Title = humanize(strings.TrimSuffix(filepath.Base(rel), ".md"))
	}
	var buf bytes.Buffer
	if err := md.Renderer().Render(&buf, source, doc); err != nil {
		slog.Error("render rules", "path", rel, "err", err)
	}
	// goldmark escapes text and drops raw HTML (unsafe is off).
	d.HTML = template.HTML(markerHTML(bookOrP(book), buf.Bytes()))
	if book == "" {
		pages = nil
	}
	return d, book, pages
}

func bookOrP(b string) string {
	if b == "" {
		return "p"
	}
	return b
}

func splitFrontMatter(src string) (frontMatter, string) {
	var fm frontMatter
	if !strings.HasPrefix(src, "---\n") {
		return fm, src
	}
	end := strings.Index(src[4:], "\n---")
	if end < 0 {
		return fm, src
	}
	// "key: value" lines; not strict YAML (titles like "Game Rules: Core Mechanics").
	for _, ln := range strings.Split(src[4:4+end], "\n") {
		k, v, ok := strings.Cut(ln, ":")
		v = strings.Trim(strings.TrimSpace(v), `"'`)
		if !ok {
			continue
		}
		switch strings.TrimSpace(k) {
		case "title":
			fm.Title = v
		case "source":
			fm.Source = v
		case "pages":
			fm.Pages = v
		}
	}
	rest := src[4+end+4:]
	return fm, strings.TrimLeft(rest, "\n")
}

func pagesLabel(s string) string {
	if s == "" {
		return ""
	}
	if strings.ContainsAny(s, "-,") {
		return "pp. " + s
	}
	return "p. " + s
}

func pageRange(a, b string) (int, int) {
	x, _ := strconv.Atoi(a)
	y, err := strconv.Atoi(b)
	if err != nil || y < x {
		y = x
	}
	return x, y
}

func nonEmpty(ss []string) []string {
	var out []string
	for _, s := range ss {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

func asBytes(v any) []byte {
	switch x := v.(type) {
	case []byte:
		return x
	case string:
		return []byte(x)
	}
	return nil
}

// plainText collects a node's text content.
func plainText(n ast.Node, src []byte) string {
	var b strings.Builder
	ast.Walk(n, func(c ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch t := c.(type) {
		case *ast.Text:
			b.Write(t.Segment.Value(src))
			if t.SoftLineBreak() {
				b.WriteByte(' ')
			}
		case *ast.String:
			b.Write(t.Value)
		}
		return ast.WalkContinue, nil
	})
	return strings.TrimSpace(b.String())
}

// ---------------------------------------------------------------- page markers

// Page markers become a placeholder paragraph before parsing, then an anchor after rendering.
var placeholder = regexp.MustCompile(`<p>HIPAGEMARK:(\d+)(?:-(\d+))?</p>`)

func markerHTML(book string, html []byte) []byte {
	return placeholder.ReplaceAllFunc(html, func(m []byte) []byte {
		sm := placeholder.FindSubmatch(m)
		a, b := pageRange(string(sm[1]), string(sm[2]))
		label := fmt.Sprintf("p. %d", a)
		if b > a {
			label = fmt.Sprintf("pp. %d-%d", a, b)
		}
		var out bytes.Buffer
		fmt.Fprintf(&out, `<div class="hi-page" id="%s" data-no-cites>`, PageAnchor(book, a))
		for p := a + 1; p <= b; p++ {
			fmt.Fprintf(&out, `<span id="%s"></span>`, PageAnchor(book, p))
		}
		fmt.Fprintf(&out, `<span>%s</span></div>`, label)
		return out.Bytes()
	})
}
