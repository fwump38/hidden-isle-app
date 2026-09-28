package web

import (
	"fmt"
	"html/template"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/fwump38/hidden-isle-app/internal/auth"
	"github.com/fwump38/hidden-isle-app/internal/rulebook"
)

// The rule browser: the loaded rules text by tab and document, with an anchor on every printed
// page. Page cites anywhere in the app link to /rules/page/{book}/{n} (app.js adds the links).
func (s *Server) registerRules(mux *http.ServeMux) {
	u := func(h http.HandlerFunc) http.Handler { return s.requireUser(h) }
	mux.Handle("GET /rules", u(s.rulesHome))
	mux.Handle("GET /rules/search", u(s.rulesSearch))
	mux.Handle("GET /rules/page/{book}/{n}", u(s.rulesPage))
	mux.Handle("GET /rules/{tab}", u(s.rulesTab))
	mux.Handle("GET /rules/{tab}/{doc}", u(s.rulesDoc))
}

type rulesPageData struct {
	Label    string // snapshot label
	Tabs     []*rulebook.Tab
	Tab      *rulebook.Tab
	Doc      *rulebook.Doc
	Prev     *rulebook.Doc
	Next     *rulebook.Doc
	ScrollTo string // element id to scroll to on load
	Notice   string

	CanSearch bool
	Query     string
	Hits      []ruleHit
	Searched  bool
}

type ruleHit struct {
	Doc      string
	Heading  string
	Page     string
	URL      string
	Snippet  template.HTML
	SeerOnly bool
}

// library returns the rules for the current snapshot, or nil if none is loaded.
func (s *Server) library() *rulebook.Library {
	if s.Data == nil {
		return nil
	}
	return s.book.For(s.Data.Current())
}

func (s *Server) renderRules(w http.ResponseWriter, r *http.Request, status int, d rulesPageData) {
	seer := auth.User(r.Context()).IsSeer()
	lib := s.library()
	d.CanSearch = s.Rules != nil
	if lib != nil {
		d.Tabs = lib.Visible(seer)
		if snap := s.Data.Current(); snap != nil {
			d.Label = snap.Label()
		}
	}
	if d.Doc != nil {
		d.Tab = d.Doc.Tab
		for i, doc := range d.Tab.Docs {
			if doc == d.Doc {
				if i > 0 {
					d.Prev = d.Tab.Docs[i-1]
				}
				if i+1 < len(d.Tab.Docs) {
					d.Next = d.Tab.Docs[i+1]
				}
			}
		}
	}
	title := "Rules"
	if d.Doc != nil {
		title = d.Doc.Title + " · Rules"
	}
	s.render(w, r, "rules", status, pageData{Title: title, Data: d})
}

// firstDoc is the first document of tab (or of the first visible tab).
func firstDoc(tabs []*rulebook.Tab, tab *rulebook.Tab) *rulebook.Doc {
	if tab == nil && len(tabs) > 0 {
		tab = tabs[0]
	}
	if tab == nil || len(tab.Docs) == 0 {
		return nil
	}
	return tab.Docs[0]
}

func (s *Server) rulesHome(w http.ResponseWriter, r *http.Request) {
	var d rulesPageData
	if lib := s.library(); lib != nil {
		d.Doc = firstDoc(lib.Visible(auth.User(r.Context()).IsSeer()), nil)
	} else {
		d.Notice = "No rules data is loaded yet."
	}
	s.renderRules(w, r, http.StatusOK, d)
}

// visibleTab returns the tab with key if the reader may see it.
func (s *Server) visibleTab(r *http.Request, key string) *rulebook.Tab {
	lib := s.library()
	if lib == nil {
		return nil
	}
	t := lib.Tab(key)
	if t == nil || (t.SeerOnly && !auth.User(r.Context()).IsSeer()) {
		return nil
	}
	return t
}

func (s *Server) rulesTab(w http.ResponseWriter, r *http.Request) {
	t := s.visibleTab(r, r.PathValue("tab"))
	if t == nil {
		s.renderRules(w, r, http.StatusNotFound, rulesPageData{Notice: "There's no such section of the rules."})
		return
	}
	s.renderRules(w, r, http.StatusOK, rulesPageData{Doc: firstDoc(nil, t)})
}

var anchorRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,120}$`)

func (s *Server) rulesDoc(w http.ResponseWriter, r *http.Request) {
	t := s.visibleTab(r, r.PathValue("tab"))
	var doc *rulebook.Doc
	if t != nil {
		doc = s.library().Doc(t.Key, r.PathValue("doc"))
	}
	if doc == nil {
		s.renderRules(w, r, http.StatusNotFound, rulesPageData{Notice: "There's no such rules document (the rules may have been updated)."})
		return
	}
	d := rulesPageData{Doc: doc}
	if at := r.URL.Query().Get("at"); anchorRe.MatchString(at) {
		d.ScrollTo = at
	}
	s.renderRules(w, r, http.StatusOK, d)
}

// rulesPage opens the document holding a printed page, scrolled to it.
func (s *Server) rulesPage(w http.ResponseWriter, r *http.Request) {
	book := r.PathValue("book")
	n, err := strconv.Atoi(r.PathValue("n"))
	lib := s.library()
	cite := citeLabel(lib, book, n)
	if err != nil || lib == nil {
		s.renderRules(w, r, http.StatusNotFound, rulesPageData{Notice: "That isn't a page of the loaded rules."})
		return
	}
	doc := lib.Page(book, n)
	if doc == nil {
		d := rulesPageData{Notice: cite + " isn't in the loaded rules text.", Doc: firstDoc(lib.Visible(auth.User(r.Context()).IsSeer()), nil)}
		s.renderRules(w, r, http.StatusNotFound, d)
		return
	}
	s.renderRules(w, r, http.StatusOK, rulesPageData{Doc: doc, ScrollTo: rulebook.PageAnchor(book, n)})
}

// citeLabel turns a book key and page back into a cite: ("sheet", 3) → "Sheet p. 3".
func citeLabel(lib *rulebook.Library, book string, n int) string {
	if lib != nil {
		for prefix, key := range lib.Cites {
			if key == book {
				return fmt.Sprintf("%s %d", prefix, n)
			}
		}
	}
	return fmt.Sprintf("p. %d", n)
}

var pageCite = regexp.MustCompile(`^pp?\.\s*(\d+)`)

func (s *Server) rulesSearch(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	d := rulesPageData{Query: q, Searched: q != ""}
	lib := s.library()
	if q == "" || s.Rules == nil || lib == nil {
		s.renderRules(w, r, http.StatusOK, d)
		return
	}
	seer := auth.User(r.Context()).IsSeer()
	hits, err := s.Rules.Search(q, seer, 30)
	if err != nil {
		d.Notice = err.Error()
	}
	for _, h := range hits {
		doc := lib.DocByPath(h.Path)
		if doc == nil || (doc.SeerOnly && !seer) {
			continue
		}
		heads := strings.Split(h.Heading, " › ")
		u := doc.URL()
		if id := doc.HeadingID(heads[len(heads)-1]); id != "" {
			u += "?at=" + id
		} else if m := pageCite.FindStringSubmatch(h.Page); m != nil && lib.Page("p", atoi(m[1])) == doc {
			u += "?at=" + rulebook.PageAnchor("p", atoi(m[1]))
		}
		// The snippet marks matches with **…**; escape it, then turn those into <mark>.
		snip := template.HTMLEscapeString(h.Snippet)
		parts := strings.Split(snip, "**")
		var b strings.Builder
		for i, p := range parts {
			if i > 0 {
				if i%2 == 1 {
					b.WriteString("<mark>")
				} else {
					b.WriteString("</mark>")
				}
			}
			b.WriteString(p)
		}
		if len(parts)%2 == 0 {
			b.WriteString("</mark>")
		}
		d.Hits = append(d.Hits, ruleHit{Doc: doc.Title, Heading: h.Heading, Page: h.Page, URL: u,
			Snippet: template.HTML(b.String()), SeerOnly: h.SeerOnly})
	}
	s.renderRules(w, r, http.StatusOK, d)
}

func atoi(s string) int { n, _ := strconv.Atoi(s); return n }
