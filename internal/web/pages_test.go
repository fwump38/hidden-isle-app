package web

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fwump38/hidden-isle-app/internal/auth"
	"github.com/fwump38/hidden-isle-app/internal/campaign"
	"github.com/fwump38/hidden-isle-app/internal/config"
	"github.com/fwump38/hidden-isle-app/internal/db"
	"github.com/fwump38/hidden-isle-app/internal/gamedata"
)

type site struct {
	t       *testing.T
	h       http.Handler
	cookies map[string]*http.Cookie
}

// get fetches path as user and returns status and body.
func (s *site) get(user, path string) (int, string) {
	r := httptest.NewRequest(http.MethodGet, path, nil)
	r.RemoteAddr = "192.168.1.10:1234"
	r.AddCookie(s.cookies[user])
	w := httptest.NewRecorder()
	s.h.ServeHTTP(w, r)
	b, _ := io.ReadAll(w.Result().Body)
	return w.Code, string(b)
}

// post submits a form as user and returns the status and the flash message, if any.
func (s *site) post(user, path string, form url.Values) (int, string) {
	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	r.RemoteAddr = "192.168.1.10:1234"
	r.AddCookie(s.cookies[user])
	w := httptest.NewRecorder()
	s.h.ServeHTTP(w, r)
	flash := ""
	for _, c := range w.Result().Cookies() {
		if c.Name == flashCookie {
			flash, _ = url.QueryUnescape(c.Value)
		}
	}
	return w.Code, flash
}

func newSite(t *testing.T) (*site, *campaign.Service) {
	t.Helper()
	dir := t.TempDir()
	g, err := db.Open(filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{LANCIDRs: []netip.Prefix{netip.MustParsePrefix("192.168.0.0/16")}}
	sess, err := auth.LoadSessions(filepath.Join(dir, "key"))
	if err != nil {
		t.Fatal(err)
	}
	data := gamedata.StaticStore(gamedata.Fixture())
	svc := campaign.New(g, data)
	srv, err := New(g, cfg, auth.New(g, cfg, sess), data, svc, "test")
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	srv.Register(mux)
	st := &site{t: t, h: http.NewCrossOriginProtection().Handler(srv.Auth.Middleware(auth.LAN)(mux)), cookies: map[string]*http.Cookie{}}
	for _, u := range []db.User{{Name: "Seer", Role: db.RoleSeer, Active: true}, {Name: "Ana", Role: db.RolePlayer, Active: true}, {Name: "Bram", Role: db.RolePlayer, Active: true}} {
		if err := g.Create(&u).Error; err != nil {
			t.Fatal(err)
		}
		rec := httptest.NewRecorder()
		sess.Issue(rec, u.ID, auth.KindLocal)
		st.cookies[u.Name] = rec.Result().Cookies()[0]
	}
	return st, svc
}

func TestPagesRenderAndKeepSecrets(t *testing.T) {
	st, _ := newSite(t)
	ok := func(code int, flash string) {
		t.Helper()
		if code != http.StatusSeeOther || flash != "" {
			t.Fatalf("post: %d %q", code, flash)
		}
	}
	ok(st.post("Seer", "/campaigns", url.Values{"name": {"Venice Nights"}, "mode": {"group"}}))
	for _, uid := range []string{"2", "3"} {
		ok(st.post("Seer", "/c/1/members", url.Values{"user_id": {uid}, "member": {"on"}}))
	}
	ok(st.post("Ana", "/c/1/agents", url.Values{"name": {"Ines"}, "class": {"prowler"}}))
	ok(st.post("Bram", "/c/1/agents", url.Values{"name": {"Cyrus"}, "class": {"occultist"}}))
	ok(st.post("Ana", "/agents/1", url.Values{"skill.Slip": {"3"}, "harm.Cups.0": {"P"}, "harm.Cups.1": {"S"}, "ability.add": {"wisp"}, "why": {"trained"}}))
	ok(st.post("Ana", "/agents/1", url.Values{"item.toggle": {"Rope"}, "prof.add": {"Illusion"}, "prof.rank": {"Adept"}}))
	ok(st.post("Ana", "/agents/1/contacts", url.Values{"set.name": {"Mother Agnese"}, "set.kind": {"Homeland"}, "set.affection": {"4"}}))
	ok(st.post("Seer", "/c/1/r/clock", url.Values{"set.name": {"SECRET-CLOCK"}, "set.segments": {"6"}, "set.scope": {"Scenario"}, "set.visibility": {"seer"}}))
	ok(st.post("Seer", "/c/1/r/clock", url.Values{"set.name": {"Guards"}, "set.segments": {"4"}, "set.scope": {"Scenario"}, "set.visibility": {"party"}}))
	ok(st.post("Seer", "/c/1/r/adversary", url.Values{"set.name": {"Death cult"}, "set.track_length": {"8"}, "set.secrets": {"SECRET-LEADER"}}))
	ok(st.post("Seer", "/c/1/r/adversary", url.Values{"set.name": {"SECRET-HIDDEN-FOE"}, "set.hidden": {"on"}}))
	ok(st.post("Seer", "/c/1/r/session", url.Values{"set.title": {"Arrival"}, "set.adventure": {"test-adventure"}}))
	ok(st.post("Seer", "/r/session/1", url.Values{"set.prep": {"SECRET-TWIST"}, "set.summary": {"Find the score"}}))
	ok(st.post("Seer", "/c/1/r/seer_note", url.Values{"set.title": {"SECRET-NOTE"}}))
	ok(st.post("Ana", "/c/1/entries", url.Values{"kind": {"journal"}, "title": {"PRIVATE-JOURNAL"}, "body": {"x"}, "visibility": {"private"}}))
	ok(st.post("Seer", "/c/1/entries", url.Values{"kind": {"recap"}, "title": {"Recap one"}, "body": {"We arrived."}, "visibility": {"party"}, "published": {"on"}}))

	// Limits and permissions come back as a flash message.
	if _, flash := st.post("Ana", "/agents/1", url.Values{"skill.Slip": {"4"}}); !strings.Contains(flash, "Slip must be 0-3") {
		t.Errorf("limit flash = %q", flash)
	}
	if _, flash := st.post("Bram", "/agents/1", url.Values{"skill.Slip": {"0"}}); flash != "You can't change that." {
		t.Errorf("permission flash = %q", flash)
	}

	pages := []string{"/", "/c/1", "/c/1/clocks", "/c/1/adversaries", "/c/1/territories", "/c/1/sessions", "/c/1/sessions/1",
		"/c/1/journal", "/c/1/rulings", "/c/1/log", "/c/1/notes", "/c/1/settings", "/agents/1", "/agents/2", "/agents/1/print",
		"/c/1/export.md", "/c/1/export.json", "/admin"}
	seerOnly := map[string]bool{"/c/1/notes": true, "/c/1/settings": true, "/admin": true}
	for _, p := range pages {
		code, body := st.get("Seer", p)
		if code != http.StatusOK {
			t.Errorf("Seer %s: %d", p, code)
		}
		if strings.Contains(body, "PRIVATE-JOURNAL") {
			t.Errorf("Seer sees Ana's private journal on %s", p)
		}
		for _, who := range []string{"Ana", "Bram"} {
			code, body := st.get(who, p)
			want := http.StatusOK
			if seerOnly[p] {
				want = http.StatusForbidden
			}
			if code != want {
				t.Errorf("%s %s: %d, want %d", who, p, code, want)
			}
			for _, secret := range []string{"SECRET-CLOCK", "SECRET-LEADER", "SECRET-HIDDEN-FOE", "SECRET-TWIST", "SECRET-NOTE"} {
				if strings.Contains(body, secret) {
					t.Errorf("%s sees %s on %s", who, secret, p)
				}
			}
			if who == "Bram" && strings.Contains(body, "PRIVATE-JOURNAL") {
				t.Errorf("Bram sees Ana's private journal on %s", p)
			}
		}
	}
	_, seerView := st.get("Seer", "/c/1/adversaries")
	if !strings.Contains(seerView, "SECRET-LEADER") || !strings.Contains(seerView, "SECRET-HIDDEN-FOE") {
		t.Error("Seer should see adversary secrets and hidden adversaries")
	}
	_, anaSheet := st.get("Ana", "/agents/1")
	for _, want := range []string{"<fieldset >", "WISP", "Mother Agnese", "2 harm: draw 1 fewer card with Cups", "Test ability text."} {
		if !strings.Contains(anaSheet, want) {
			t.Errorf("Ana's sheet is missing %q", want)
		}
	}
	if _, bramView := st.get("Bram", "/agents/1"); !strings.Contains(bramView, "<fieldset disabled>") {
		t.Error("Bram's view of Ana's sheet should be read-only")
	}
	_, log := st.get("Ana", "/c/1/log")
	for _, want := range []string{"Slip 2 → 3", "harm Cups — → P S", "abilities &#43; wisp", "trained"} {
		if !strings.Contains(log, want) {
			t.Errorf("log is missing %q", want)
		}
	}
}

func TestUndoFromTheLog(t *testing.T) {
	st, svc := newSite(t)
	st.post("Seer", "/campaigns", url.Values{"name": {"C"}, "mode": {"group"}})
	st.post("Seer", "/c/1/members", url.Values{"user_id": {"2"}, "member": {"on"}})
	st.post("Ana", "/c/1/agents", url.Values{"name": {"Ines"}, "class": {"prowler"}})
	st.post("Ana", "/agents/1", url.Values{"set.burden_track": {"3"}})
	var seer db.User
	svc.DB.First(&seer, 1)
	evs, _ := svc.Events(campaign.Actor{User: &seer}, 1, campaign.EventFilter{EntityType: "agent", Limit: 1})
	if code, flash := st.post("Ana", fmt.Sprintf("/events/%d/undo", evs[0].ID), url.Values{"back": {"/c/1/log"}}); code != http.StatusSeeOther || flash != "" {
		t.Fatalf("undo: %d %q", code, flash)
	}
	ag, _ := svc.Agent(campaign.Actor{User: &seer}, 1)
	if ag.BurdenTrack != 0 {
		t.Errorf("burden track = %d after undo", ag.BurdenTrack)
	}
}

func TestPlayerAgentFlow(t *testing.T) {
	st, svc := newSite(t)
	ok := func(code int, flash string) {
		t.Helper()
		if code != http.StatusSeeOther || flash != "" {
			t.Fatalf("post: %d %q", code, flash)
		}
	}
	// A new player, in no campaign, sees My Agents and can create one.
	if code, body := st.get("Ana", "/"); code != http.StatusOK || !strings.Contains(body, "My Agents") || !strings.Contains(body, "Create Agent") {
		t.Fatalf("home for a player without a campaign: %d", code)
	}
	ok(st.post("Ana", "/agents", url.Values{"name": {"Ines"}, "class": {"prowler"}, "campaign_id": {"0"}}))
	if code, body := st.get("Ana", "/agents/1"); code != http.StatusOK || !strings.Contains(body, "Not in a campaign") {
		t.Fatalf("private sheet: %d", code)
	}
	if code, _ := st.get("Bram", "/agents/1"); code != http.StatusNotFound {
		t.Errorf("another player opened a private Agent: %d", code)
	}
	ok(st.post("Ana", "/agents/1", url.Values{"skill.Slip": {"3"}}))

	// The Seer makes a campaign and adds Ana from the overview.
	ok(st.post("Seer", "/campaigns", url.Values{"name": {"Venice"}, "mode": {"group"}}))
	_, overview := st.get("Seer", "/c/1")
	if !strings.Contains(overview, "Players") || !strings.Contains(overview, ">Ana</option>") {
		t.Fatal("Seer overview should offer to add Ana")
	}
	ok(st.post("Seer", "/c/1/members", url.Values{"user_id": {"2"}, "member": {"on"}, "back": {"/c/1"}}))

	// Ana brings Ines in; Bram (not a member) still can't see her.
	if _, body := st.get("Ana", "/c/1"); !strings.Contains(body, "Bring in") {
		t.Fatal("Ana should be offered to bring her Agent in")
	}
	ok(st.post("Ana", "/c/1/bring", url.Values{"agent_id": {"1"}}))
	if _, body := st.get("Ana", "/c/1"); !strings.Contains(body, "Ines") {
		t.Error("Ines should be listed in the campaign")
	}
	if code, _ := st.get("Bram", "/agents/1"); code != http.StatusForbidden && code != http.StatusNotFound {
		t.Errorf("non-member opened a campaign Agent: %d", code)
	}
	ok(st.post("Ana", "/agents/1", url.Values{"set.status": {"Resting"}}))

	// The Seer creates an Agent for Bram, which adds him to the campaign.
	ok(st.post("Seer", "/c/1/agents", url.Values{"name": {"Cyrus"}, "class": {"occultist"}, "owner_id": {"3"}}))
	if code, _ := st.get("Bram", "/c/1"); code != http.StatusOK {
		t.Errorf("Bram should now be in the campaign: %d", code)
	}
	if code, _ := st.post("Bram", "/agents/1", url.Values{"set.status": {"Active"}}); code != http.StatusSeeOther {
		t.Fatal(code)
	}
	var seer db.User
	svc.DB.First(&seer, 1)
	ines, _ := svc.Agent(campaign.Actor{User: &seer}, 1)
	if ines.Status != "Resting" {
		t.Errorf("Bram changed Ana's Agent: status %s", ines.Status)
	}

	// Deleting: Ana deletes her Agent; the Seer deletes the campaign (with its name) and Bram.
	ok(st.post("Ana", "/agents/1/delete", nil))
	if _, flash := st.post("Seer", "/c/1/delete", url.Values{"confirm": {"wrong"}}); !strings.Contains(flash, "confirm") {
		t.Errorf("campaign delete without confirmation: %q", flash)
	}
	ok(st.post("Seer", "/c/1/delete", url.Values{"confirm": {"Venice"}}))
	if code, _ := st.get("Seer", "/c/1"); code != http.StatusNotFound {
		t.Errorf("campaign still there: %d", code)
	}
	if code, _ := st.post("Seer", "/admin/users/3/delete", nil); code != http.StatusOK {
		t.Errorf("delete player: %d", code)
	}
	var n int64
	svc.DB.Model(&db.User{}).Where("id = ?", 3).Count(&n)
	if n != 0 {
		t.Error("Bram still exists")
	}
}
