package web

import (
	"encoding/json"
	"fmt"
	"html/template"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/fwump38/hidden-isle-app/internal/db"
	"github.com/fwump38/hidden-isle-app/internal/gamedata"
)

func templateFuncs() template.FuncMap {
	return template.FuncMap{
		"seq": func(from, to int) []int { // inclusive
			var out []int
			for i := from; i <= to; i++ {
				out = append(out, i)
			}
			return out
		},
		"since": func(t time.Time) string {
			if t.IsZero() {
				return "never"
			}
			return time.Since(t).Round(time.Second).String() + " ago"
		},
		"short": func(s string) string {
			if len(s) > 12 {
				return s[:12]
			}
			return s
		},
		"deref": func(p *string) string {
			if p == nil {
				return ""
			}
			return *p
		},
		"dict": func(kv ...any) (map[string]any, error) {
			if len(kv)%2 != 0 {
				return nil, fmt.Errorf("dict needs key/value pairs")
			}
			m := map[string]any{}
			for i := 0; i < len(kv); i += 2 {
				k, ok := kv[i].(string)
				if !ok {
					return nil, fmt.Errorf("dict keys must be strings")
				}
				m[k] = kv[i+1]
			}
			return m, nil
		},
		"list": func(s ...string) []string { return s },
		"json": func(v any) string {
			b, _ := json.Marshal(v)
			return string(b)
		},
		"dict2": func(k, v string) map[string]string { return map[string]string{k: v} },
		// fill encodes a wizard choice's form values for app.js's [data-fill] handler.
		"fill": func(m map[string]string) string {
			b, _ := json.Marshal(m)
			return string(b)
		},
		// help renders a small "?" that shows text on hover, focus or tap.
		"help": func(text string) template.HTML {
			// app.js shows it in a popover, with any page cites linked to the rule browser.
			return template.HTML(`<i class="bi bi-question-circle hi-help" tabindex="0" role="button" data-hi-help="` +
				template.HTMLEscapeString(text) + `" aria-label="` + template.HTMLEscapeString(text) + `"></i>`)
		},
		"add":      func(a, b int) int { return a + b },
		"sub":      func(a, b int) int { return a - b },
		"contains": func(list []string, s string) bool { return slices.Contains(list, s) },
		"join":     strings.Join,
		"lines":    func(list []string) string { return strings.Join(list, "\n") },
		"date": func(t any) string {
			switch v := t.(type) {
			case time.Time:
				return v.Format("2006-01-02")
			case *time.Time:
				if v != nil {
					return v.Format("2006-01-02")
				}
			}
			return ""
		},
		"when":  func(t time.Time) string { return t.Local().Format("Jan 2, 15:04") },
		"clock": clockSVG,
		"uintv": func(p *uint) uint {
			if p == nil {
				return 0
			}
			return *p
		},
		"isSeerVis": func(v db.Visibility) bool { return v == db.VisSeer },
		"pct": func(n, of int) int {
			if of <= 0 {
				return 0
			}
			return n * 100 / of
		},
		"className": func(snap *gamedata.Snapshot, id string) string {
			if snap != nil {
				if c := snap.Class(id); c != nil {
					return c.Name
				}
			}
			return id
		},
		"harmCount": func(h map[string][]string) int {
			n := 0
			for _, m := range h {
				n += len(m)
			}
			return n
		},
		"changeVal": func(r json.RawMessage) string { return show(r) },
		"adventureTitle": func(snap *gamedata.Snapshot, id string) string {
			if snap == nil || id == "" {
				return ""
			}
			for _, a := range snap.Adventures.Adventures {
				if a.ID == id {
					return a.Title
				}
			}
			return id
		},
	}
}

// show renders a change-log value briefly.
func show(r json.RawMessage) string {
	s := string(r)
	if s == "" || s == "null" || s == `""` || s == "[]" || s == "{}" {
		return "—"
	}
	var str string
	if json.Unmarshal(r, &str) == nil {
		if len([]rune(str)) > 80 {
			return string([]rune(str)[:79]) + "…"
		}
		return str
	}
	if len(s) > 120 {
		return s[:119] + "…"
	}
	return s
}

// clockSVG draws a Forged-in-the-Dark clock: a circle cut into segments, filled ones shaded.
func clockSVG(filled, segments, size int) template.HTML {
	if segments <= 0 {
		return ""
	}
	r := float64(size)/2 - 2
	c := float64(size) / 2
	var b strings.Builder
	fmt.Fprintf(&b, `<svg class="hi-clock" width="%d" height="%d" viewBox="0 0 %d %d" role="img" aria-label="%d of %d segments filled">`, size, size, size, size, filled, segments)
	for i := 0; i < segments; i++ {
		a0 := 2*math.Pi*float64(i)/float64(segments) - math.Pi/2
		a1 := 2*math.Pi*float64(i+1)/float64(segments) - math.Pi/2
		x0, y0 := c+r*math.Cos(a0), c+r*math.Sin(a0)
		x1, y1 := c+r*math.Cos(a1), c+r*math.Sin(a1)
		cls := "seg"
		if i < filled {
			cls = "seg filled"
		}
		fmt.Fprintf(&b, `<path class="%s" d="M%.2f %.2f L%.2f %.2f A%.2f %.2f 0 0 1 %.2f %.2f Z"/>`, cls, c, c, x0, y0, r, r, x1, y1)
	}
	b.WriteString(`</svg>`)
	return template.HTML(b.String())
}
