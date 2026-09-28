package web

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/fwump38/hidden-isle-app/internal/assist"
	"github.com/fwump38/hidden-isle-app/internal/auth"
	"github.com/fwump38/hidden-isle-app/internal/db"
	"github.com/fwump38/hidden-isle-app/internal/gamedata"
	"github.com/fwump38/hidden-isle-app/internal/mcpsrv"
)

// ---------------------------------------------------------------- login

type loginData struct {
	Users      []db.User
	PINAllowed bool
	OIDC       bool
	Next       string
}

func (s *Server) loginPage(w http.ResponseWriter, r *http.Request) {
	if auth.User(r.Context()) != nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	s.renderLogin(w, r, http.StatusOK, "")
}

func (s *Server) renderLogin(w http.ResponseWriter, r *http.Request, status int, errMsg string) {
	ld := loginData{PINAllowed: s.Auth.PINAllowed(r), OIDC: s.Auth.OIDC != nil, Next: auth.SafeNext(r.FormValue("next"))}
	if ld.PINAllowed {
		s.DB.Where("active = ?", true).Order("role desc, name").Find(&ld.Users)
	}
	if err := auth.Info(r.Context()).Err; err != nil && errMsg == "" && !errors.Is(err, auth.ErrSSORequired) {
		errMsg = err.Error()
	}
	s.render(w, r, "login", status, pageData{Title: "Sign in", Error: errMsg, Data: ld})
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseUint(r.FormValue("user_id"), 10, 64)
	if _, err := s.Auth.Login(w, r, uint(id), r.FormValue("secret")); err != nil {
		s.renderLogin(w, r, http.StatusUnauthorized, err.Error())
		return
	}
	http.Redirect(w, r, auth.SafeNext(r.FormValue("next")), http.StatusSeeOther)
}

func (s *Server) oidcStart(w http.ResponseWriter, r *http.Request) {
	if s.Auth.OIDC == nil {
		s.renderLogin(w, r, http.StatusNotFound, "Sign in with Authentik isn't set up (HI_OAUTH_CLIENT_SECRET).")
		return
	}
	if err := s.Auth.OIDC.Start(w, r, r.FormValue("next")); err != nil {
		s.renderLogin(w, r, http.StatusBadGateway, err.Error())
	}
}

func (s *Server) oidcCallback(w http.ResponseWriter, r *http.Request) {
	if s.Auth.OIDC == nil {
		http.NotFound(w, r)
		return
	}
	next, err := s.Auth.OIDC.Callback(w, r)
	if err != nil {
		s.render(w, r, "denied", http.StatusForbidden, pageData{Title: "Sign-in failed", Error: err.Error()})
		return
	}
	http.Redirect(w, r, next, http.StatusSeeOther)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	s.Auth.Sessions.Clear(w)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// ---------------------------------------------------------------- home

type homeData struct {
	Snapshot  *gamedata.Snapshot
	Updates   []string
	Campaigns []db.Campaign
	Modes     []string
	Agents    []db.Agent      // the player's own Agents
	CampNames map[uint]string // campaign id → name
}

func (s *Server) home(w http.ResponseWriter, r *http.Request) {
	a := s.actor(r)
	d := homeData{Snapshot: s.Data.Current()}
	d.Campaigns, _ = s.Svc.Campaigns(a)
	d.Agents, _ = s.Svc.MyAgents(a)
	d.CampNames = map[uint]string{}
	for _, c := range d.Campaigns {
		d.CampNames[c.ID] = c.Name
	}
	if d.Snapshot != nil {
		d.Modes = d.Snapshot.Campaign.CampaignMode
	}
	if a.IsSeer() {
		d.Updates = s.Data.Status().Updates
	}
	s.render(w, r, "home", http.StatusOK, pageData{Title: "The Hidden Isle", Error: takeFlash(w, r), Data: d})
}

// ---------------------------------------------------------------- admin

type adminData struct {
	Home      *auth.HomeStatus
	Me        auth.RequestInfo
	Status    gamedata.Status
	Snapshot  *gamedata.Snapshot
	Users     []db.User
	Tokens    []db.APIToken
	NewToken  string
	MCPURL    string
	DeployKey string
	KeyError  string

	AssistEnabled      bool
	AssistMonth        string
	AssistGlobalUSD    float64
	AssistGlobalCapUSD float64
	AssistUsage        []assist.UsageRow
}

func (s *Server) adminDataFor(r *http.Request) adminData {
	ad := s.adminData()
	ad.Me = auth.Info(r.Context())
	return ad
}

func (s *Server) adminData() adminData {
	ad := adminData{Status: s.Data.Status(), Snapshot: s.Data.Current(), MCPURL: s.Cfg.PublicURL + "/mcp"}
	if s.Auth.Home != nil {
		st := s.Auth.Home.Status()
		ad.Home = &st
	}
	if pub, err := s.Data.DeployPublicKey(); err != nil {
		ad.KeyError = err.Error()
	} else {
		ad.DeployKey = pub
	}
	s.DB.Order("role desc, name").Find(&ad.Users)
	s.DB.Where("revoked_at IS NULL").Order("created_at desc").Find(&ad.Tokens)
	if s.Assist != nil {
		ad.AssistEnabled = true
		ad.AssistGlobalCapUSD = s.Cfg.AssistMonthlyCapUSD
		ad.AssistMonth, ad.AssistGlobalUSD, ad.AssistUsage = s.Assist.UsageSummary()
	}
	return ad
}

func (s *Server) admin(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, "admin", http.StatusOK, pageData{Title: "Admin", Data: s.adminDataFor(r)})
}

// homeNetwork marks (or forgets) a network as home for tunnel requests.
func (s *Server) homeNetwork(w http.ResponseWriter, r *http.Request) {
	if s.Auth.Home == nil {
		s.adminFlash(w, r, "", "home detection is off (HI_HOME_DETECT=off)", s.adminDataFor(r))
		return
	}
	if f := r.FormValue("forget"); f != "" {
		p, err := netip.ParsePrefix(f)
		if err == nil {
			err = s.Auth.Home.Forget(p)
		}
		if err != nil {
			s.adminFlash(w, r, "", err.Error(), s.adminDataFor(r))
			return
		}
		s.adminFlash(w, r, "Forgot "+f+".", "", s.adminDataFor(r))
		return
	}
	info := auth.Info(r.Context())
	if !info.Tunnel {
		s.adminFlash(w, r, "", "Open this page through https://your-domain (not the local IP) on the home Wi-Fi, then press the button: the app needs to see the address Cloudflare reports.", s.adminDataFor(r))
		return
	}
	p, err := s.Auth.Home.Learn(info.Client)
	if err != nil {
		s.adminFlash(w, r, "", err.Error(), s.adminDataFor(r))
		return
	}
	s.adminFlash(w, r, "Requests from "+p.String()+" now count as home.", "", s.adminDataFor(r))
}

func (s *Server) deleteUser(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseUint(r.PathValue("id"), 10, 64)
	var u db.User
	s.DB.First(&u, id)
	if err := s.Svc.DeleteUser(s.actor(r), uint(id)); err != nil {
		s.adminFlash(w, r, "", friendly(err), s.adminDataFor(r))
		return
	}
	s.adminFlash(w, r, "Deleted "+u.Name+".", "", s.adminDataFor(r))
}

func (s *Server) syncGameData(w http.ResponseWriter, r *http.Request) {
	_ = s.Data.Sync(r.Context()) // the error is in Status
	s.partial(w, "admin", "gamedata", s.adminDataFor(r))
}

func (s *Server) installGameData(w http.ResponseWriter, r *http.Request) {
	tag := r.FormValue("tag")
	if err := s.Data.Install(r.Context(), tag); err != nil {
		s.adminFlash(w, r, "", err.Error(), s.adminDataFor(r))
		return
	}
	s.adminFlash(w, r, "Rules data is now "+tag+".", "", s.adminDataFor(r))
}

func (s *Server) adminFlash(w http.ResponseWriter, r *http.Request, flash, errMsg string, ad adminData) {
	status := http.StatusOK
	if errMsg != "" {
		status = http.StatusUnprocessableEntity
	}
	s.render(w, r, "admin", status, pageData{Title: "Admin", Flash: flash, Error: errMsg, Data: ad})
}

func (s *Server) addUser(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.FormValue("name"))
	pin := r.FormValue("pin")
	u := db.User{Name: name, Role: db.RolePlayer, Active: true, Email: optEmail(r.FormValue("email"))}
	var err error
	switch {
	case name == "":
		err = errors.New("name is required")
	case pin != "" && !auth.ValidPIN(pin):
		err = errors.New("PIN must be 4-8 digits")
	case pin == "" && u.Email == nil:
		err = errors.New("give the player a PIN, an SSO email, or both")
	}
	if err == nil && pin != "" {
		u.PINHash, err = auth.Hash(pin)
	}
	if err == nil {
		if err = s.DB.Create(&u).Error; db.IsUniqueViolation(err) {
			err = errors.New("that name or email is already in use")
		}
	}
	if err != nil {
		s.adminFlash(w, r, "", err.Error(), s.adminDataFor(r))
		return
	}
	s.adminFlash(w, r, "Added "+u.Name+".", "", s.adminDataFor(r))
}

func (s *Server) updateUser(w http.ResponseWriter, r *http.Request) {
	var u db.User
	if err := s.DB.First(&u, r.PathValue("id")).Error; err != nil {
		http.NotFound(w, r)
		return
	}
	me := auth.User(r.Context())
	var err error
	switch r.FormValue("action") {
	case "pin":
		pin := r.FormValue("pin")
		if u.IsSeer() {
			if len(pin) < 8 {
				err = errors.New("the Seer's password needs at least 8 characters")
			} else {
				u.PasswordHash, err = auth.Hash(pin)
			}
		} else if !auth.ValidPIN(pin) {
			err = errors.New("PIN must be 4-8 digits")
		} else {
			u.PINHash, err = auth.Hash(pin)
		}
	case "email":
		u.Email = optEmail(r.FormValue("email"))
		if u.IsSeer() && u.ID == me.ID && u.Email == nil && u.PasswordHash == "" {
			err = errors.New("the Seer needs an SSO email or a password")
		}
	case "toggle":
		if u.ID == me.ID {
			err = errors.New("you can't deactivate yourself")
		}
		u.Active = !u.Active
	default:
		err = errors.New("unknown action")
	}
	if err == nil {
		if err = s.DB.Save(&u).Error; db.IsUniqueViolation(err) {
			err = errors.New("that email is already in use")
		}
	}
	if err != nil {
		s.adminFlash(w, r, "", err.Error(), s.adminDataFor(r))
		return
	}
	s.adminFlash(w, r, "Updated "+u.Name+".", "", s.adminDataFor(r))
}

func (s *Server) createToken(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		name = "Claude Code"
	}
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	token := mcpsrv.TokenPrefix + base64.RawURLEncoding.EncodeToString(raw)
	t := db.APIToken{UserID: auth.User(r.Context()).ID, Name: name, Hash: mcpsrv.HashToken(token), Prefix: token[:10]}
	if err := s.DB.Create(&t).Error; err != nil {
		s.adminFlash(w, r, "", err.Error(), s.adminDataFor(r))
		return
	}
	ad := s.adminDataFor(r)
	ad.NewToken = token
	s.adminFlash(w, r, "Token created. Copy it now: it won't be shown again.", "", ad)
}

func (s *Server) revokeToken(w http.ResponseWriter, r *http.Request) {
	now := time.Now()
	s.DB.Model(&db.APIToken{}).Where("id = ?", r.PathValue("id")).Update("revoked_at", &now)
	s.adminFlash(w, r, "Token revoked.", "", s.adminDataFor(r))
}

func optEmail(s string) *string {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return nil
	}
	return &s
}
