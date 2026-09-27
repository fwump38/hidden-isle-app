// Package web serves the browser UI: Bootstrap 5 (dark) pages with htmx for partial updates.
package web

import (
	"bytes"
	"embed"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"path"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/fwump38/hidden-isle-app/internal/auth"
	"github.com/fwump38/hidden-isle-app/internal/config"
	"github.com/fwump38/hidden-isle-app/internal/db"
	"github.com/fwump38/hidden-isle-app/internal/gamedata"
)

//go:embed templates static
var assets embed.FS

type Server struct {
	DB    *gorm.DB
	Cfg   *config.Config
	Auth  *auth.Authenticator
	Data  *gamedata.Store
	Build string

	pages map[string]*template.Template
}

func New(g *gorm.DB, cfg *config.Config, a *auth.Authenticator, data *gamedata.Store, build string) (*Server, error) {
	s := &Server{DB: g, Cfg: cfg, Auth: a, Data: data, Build: build, pages: map[string]*template.Template{}}
	funcs := template.FuncMap{
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
	}
	pages, err := fs.Glob(assets, "templates/pages/*.html")
	if err != nil {
		return nil, err
	}
	for _, p := range pages {
		t, err := template.New("").Funcs(funcs).ParseFS(assets, "templates/layout.html", "templates/partials/*.html", p)
		if err != nil {
			return nil, err
		}
		s.pages[strings.TrimSuffix(path.Base(p), ".html")] = t
	}
	return s, nil
}

// Register mounts the UI routes on mux.
func (s *Server) Register(mux *http.ServeMux) {
	static, _ := fs.Sub(assets, "static")
	mux.Handle("GET /static/", http.StripPrefix("/static/", cacheForever(http.FileServerFS(static))))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })

	mux.HandleFunc("GET /login", s.loginPage)
	mux.HandleFunc("POST /login", s.login)
	mux.HandleFunc("POST /logout", s.logout)

	mux.Handle("GET /{$}", s.requireUser(http.HandlerFunc(s.home)))

	mux.Handle("GET /admin", s.requireSeer(http.HandlerFunc(s.admin)))
	mux.Handle("POST /admin/gamedata/sync", s.requireSeer(http.HandlerFunc(s.syncGameData)))
	mux.Handle("POST /admin/users", s.requireSeer(http.HandlerFunc(s.addUser)))
	mux.Handle("POST /admin/users/{id}", s.requireSeer(http.HandlerFunc(s.updateUser)))
	mux.Handle("POST /admin/tokens", s.requireSeer(http.HandlerFunc(s.createToken)))
	mux.Handle("POST /admin/tokens/{id}/revoke", s.requireSeer(http.HandlerFunc(s.revokeToken)))
}

func cacheForever(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=86400")
		h.ServeHTTP(w, r)
	})
}

// pageData is what every page template receives.
type pageData struct {
	Title string
	User  *db.User
	Info  auth.RequestInfo
	Build string
	Flash string
	Error string
	Data  any
}

func (s *Server) render(w http.ResponseWriter, r *http.Request, page string, status int, pd pageData) {
	pd.User = auth.User(r.Context())
	pd.Info = auth.Info(r.Context())
	pd.Build = s.Build
	t, ok := s.pages[page]
	if !ok {
		http.Error(w, "no page "+page, http.StatusInternalServerError)
		return
	}
	name := "layout"
	if r.Header.Get("HX-Request") == "true" && r.Header.Get("HX-Target") != "" {
		name = "content" // htmx swaps only the page body
	}
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, name, pd); err != nil {
		slog.Error("render", "page", page, "err", err)
		http.Error(w, "template error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	buf.WriteTo(w)
}

// partial renders one named template (for htmx swaps).
func (s *Server) partial(w http.ResponseWriter, page, name string, data any) {
	var buf bytes.Buffer
	if err := s.pages[page].ExecuteTemplate(&buf, name, data); err != nil {
		slog.Error("render partial", "name", name, "err", err)
		http.Error(w, "template error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	buf.WriteTo(w)
}

func (s *Server) requireUser(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if auth.User(r.Context()) != nil {
			next.ServeHTTP(w, r)
			return
		}
		info := auth.Info(r.Context())
		if info.Tunnel || info.Err != nil {
			msg := "You need to sign in."
			if info.Err != nil {
				msg = info.Err.Error()
			}
			s.render(w, r, "denied", http.StatusForbidden, pageData{Title: "Not signed in", Error: msg})
			return
		}
		http.Redirect(w, r, "/login", http.StatusSeeOther)
	})
}

func (s *Server) requireSeer(next http.Handler) http.Handler {
	return s.requireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !auth.User(r.Context()).IsSeer() {
			s.render(w, r, "denied", http.StatusForbidden, pageData{Title: "Seer only", Error: "Only the Seer can open this page."})
			return
		}
		next.ServeHTTP(w, r)
	}))
}
