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
	"time"

	"github.com/fwump38/hidden-isle-app/internal/auth"
	"github.com/fwump38/hidden-isle-app/internal/campaign"
	"github.com/fwump38/hidden-isle-app/internal/config"
	"github.com/fwump38/hidden-isle-app/internal/db"
	"github.com/fwump38/hidden-isle-app/internal/gamedata"
	"github.com/fwump38/hidden-isle-app/internal/live"
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

// post submits a form as user and returns the status and the flash message, if any. Use this
// for the usual redirect-on-POST handlers.
func (s *site) post(user, path string, form url.Values) (int, string) {
	w := s.doPost(user, path, form)
	flash := ""
	for _, c := range w.Result().Cookies() {
		if c.Name == flashCookie {
			flash, _ = url.QueryUnescape(c.Value)
		}
	}
	return w.Code, flash
}

// postBody submits a form as user and returns the status and the response body, for handlers
// that render a partial directly (htmx swaps) instead of redirecting.
func (s *site) postBody(user, path string, form url.Values) (int, string) {
	w := s.doPost(user, path, form)
	b, _ := io.ReadAll(w.Result().Body)
	return w.Code, string(b)
}

// postLocation submits a form as user and returns the status and where it redirects.
func (s *site) postLocation(user, path string, form url.Values) (int, string) {
	w := s.doPost(user, path, form)
	return w.Code, w.Header().Get("Location")
}

// htmxPost submits a form the way htmx does and returns the status, body and response headers.
func (s *site) htmxPost(user, path string, form url.Values) (int, string, http.Header) {
	w := s.doPost(user, path, form, "HX-Request", "true")
	b, _ := io.ReadAll(w.Result().Body)
	return w.Code, string(b), w.Header()
}

func (s *site) doPost(user, path string, form url.Values, headers ...string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	for i := 0; i+1 < len(headers); i += 2 {
		r.Header.Set(headers[i], headers[i+1])
	}
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	r.RemoteAddr = "192.168.1.10:1234"
	r.AddCookie(s.cookies[user])
	w := httptest.NewRecorder()
	s.h.ServeHTTP(w, r)
	return w
}

func newSite(t *testing.T) (*site, *campaign.Service) {
	t.Helper()
	st, svc, _ := newSiteWithServer(t)
	return st, svc
}

func newSiteWithServer(t *testing.T) (*site, *campaign.Service, *Server) {
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
	srv.Live = live.New()
	svc.OnEvent = func(ev db.Event) { PublishEvent(srv.Live, ev) }
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
	return st, svc, srv
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

func TestChallengeHelper(t *testing.T) {
	st, _ := newSite(t)
	st.post("Ana", "/agents", url.Values{"name": {"Ines"}, "class": {"prowler"}, "campaign_id": {"0"}}) // Slip 2 pre-filled
	st.post("Ana", "/agents/1", url.Values{"harm.Cups.0": {"P"}, "harm.Cups.1": {"S"}})
	body := func(form url.Values) string {
		r := httptest.NewRequest(http.MethodPost, "/agents/1/challenge", strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("Sec-Fetch-Site", "same-origin")
		r.RemoteAddr = "192.168.1.10:1"
		r.AddCookie(st.cookies["Ana"])
		w := httptest.NewRecorder()
		st.h.ServeHTTP(w, r)
		return w.Body.String()
	}
	out := body(url.Values{"skill": {"Slip"}, "difficulty": {"hard"}, "burden": {"on"}, "action": {"count"}})
	// 1 + 2 Slip + 1 burden - 1 (2 harm in Cups) = 3; Seer 4.
	if !strings.Contains(out, `<div class="fs-4 fw-bold">3</div>cards for you`) || !strings.Contains(out, `<div class="fs-4 fw-bold">4</div>cards for the Seer`) {
		t.Errorf("count output: %s", out)
	}
	if !strings.Contains(out, "Mark the burden track") {
		t.Error("should offer to mark the burden track")
	}
	out = body(url.Values{"skill": {"Slip"}, "difficulty": {"medium"}, "action": {"resolve"}, "played": {"3 of Cups"}, "seer": {"10 of Pentacles"}})
	if !strings.Contains(out, "Total success") {
		t.Errorf("resolve: %s", out)
	}
	out = body(url.Values{"skill": {"Slip"}, "difficulty": {"medium"}, "action": {"resolve"}, "played": {"3 of Wands"}, "seer": {"10 of Pentacles"}})
	if !strings.Contains(out, "Failure") || !strings.Contains(out, "Consequence ideas") {
		t.Errorf("failure: %s", out)
	}
}

func TestOraclePage(t *testing.T) {
	st, _ := newSite(t)
	st.post("Seer", "/campaigns", url.Values{"name": {"C"}, "mode": {"group"}})
	if code, body := st.get("Seer", "/c/1/oracle"); code != http.StatusOK || !strings.Contains(body, "Yes or no?") {
		t.Fatalf("oracle page: %d", code)
	}
	read := func(form url.Values) string {
		r := httptest.NewRequest(http.MethodPost, "/c/1/oracle", strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("Sec-Fetch-Site", "same-origin")
		r.RemoteAddr = "192.168.1.10:1"
		r.AddCookie(st.cookies["Seer"])
		w := httptest.NewRecorder()
		st.h.ServeHTTP(w, r)
		return w.Body.String()
	}
	out := read(url.Values{"tool": {"closed"}, "likelihood": {"50-50"}, "yes0": {"4 of Cups"}, "no0": {"9 of Cups"}, "action": {"read"}})
	if !strings.Contains(out, ">No") || !strings.Contains(out, "extreme") {
		t.Errorf("closed: %s", out)
	}
	out = read(url.Values{"tool": {"event"}, "card": {"6 of Swords"}, "action": {"read"}})
	if !strings.Contains(out, "present") || !strings.Contains(out, "test present") {
		t.Errorf("event: %s", out)
	}
	out = read(url.Values{"tool": {"npc"}, "action": {"draw"}, "region": {"Venice"}})
	if !strings.Contains(out, "Drawn:") || !strings.Contains(out, "Name ideas") {
		t.Errorf("npc draw: %s", out)
	}
	if code, _ := st.get("Ana", "/c/1/oracle"); code != http.StatusForbidden {
		t.Errorf("non-member opened the oracle: %d", code)
	}
}

func TestLiveTableAndHandouts(t *testing.T) {
	st, svc := newSite(t)
	st.post("Seer", "/campaigns", url.Values{"name": {"C"}, "mode": {"group"}})
	for _, uid := range []string{"2", "3"} {
		st.post("Seer", "/c/1/members", url.Values{"user_id": {uid}, "member": {"on"}})
	}
	if code, _ := st.get("Seer", "/c/1/play"); code != http.StatusOK {
		t.Fatalf("dashboard: %d", code)
	}
	if code, _ := st.get("Ana", "/c/1/play"); code != http.StatusForbidden {
		t.Errorf("player opened the dashboard: %d", code)
	}

	// Ana listens on the live stream through a real server.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.RemoteAddr = "192.168.1.10:1"
		st.h.ServeHTTP(w, r)
	}))
	defer ts.Close()
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/c/1/live", nil)
	req.AddCookie(st.cookies["Ana"])
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("live stream: %v %v", err, resp)
	}
	defer resp.Body.Close()
	lines := make(chan string, 100)
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := resp.Body.Read(buf)
			if n > 0 {
				lines <- string(buf[:n])
			}
			if err != nil {
				close(lines)
				return
			}
		}
	}()
	for svcSubs(st) == 0 {
		time.Sleep(10 * time.Millisecond)
	}

	st.post("Seer", "/c/1/r/clock", url.Values{"set.name": {"SECRET-CLOCK"}, "set.segments": {"4"}, "set.scope": {"Scenario"}, "set.visibility": {"seer"}})
	st.post("Seer", "/c/1/handouts", url.Values{"title": {"For Bram only"}, "to": {"3"}})
	st.post("Seer", "/c/1/r/clock", url.Values{"set.name": {"Guards"}, "set.segments": {"4"}, "set.scope": {"Scenario"}, "set.visibility": {"party"}})
	st.post("Seer", "/c/1/handouts", url.Values{"title": {"A sealed letter"}, "body": {"Meet at dawn."}, "card": {"Page of Cups"}, "on_table": {"on"}})

	var got strings.Builder
	deadline := time.After(3 * time.Second)
	for !strings.Contains(got.String(), "A sealed letter") {
		select {
		case l, ok := <-lines:
			if !ok {
				t.Fatal("stream closed")
			}
			got.WriteString(l)
		case <-deadline:
			t.Fatalf("no handout on the stream: %q", got.String())
		}
	}
	stream := got.String()
	if !strings.Contains(stream, "event: changed") {
		t.Error("public change not announced")
	}
	if strings.Contains(stream, "For Bram only") || strings.Count(stream, "event: changed") != 1 {
		t.Errorf("Ana's stream got something she shouldn't: %q", stream)
	}

	// The TV view: only with the key, and only public things.
	var seer db.User
	svc.DB.First(&seer, 1)
	key, _ := svc.TableKey(campaign.Actor{User: &seer}, 1, false)
	if code, _ := st.get("Ana", "/table/1?key=wrong"); code != http.StatusNotFound {
		t.Errorf("TV with a bad key: %d", code)
	}
	r := httptest.NewRequest(http.MethodGet, "/table/1?key="+key, nil)
	r.RemoteAddr = "192.168.1.50:1"
	w := httptest.NewRecorder()
	st.h.ServeHTTP(w, r) // no cookie: the TV isn't signed in
	tv := w.Body.String()
	if w.Code != http.StatusOK || !strings.Contains(tv, "Guards") || !strings.Contains(tv, "A sealed letter") || strings.Contains(tv, "SECRET") || strings.Contains(tv, "For Bram only") {
		t.Errorf("TV view (%d): %s", w.Code, tv)
	}
	_, over := st.get("Ana", "/c/1")
	if !strings.Contains(over, "A sealed letter") || strings.Contains(over, "For Bram only") {
		t.Error("Ana's handouts list")
	}
}

func svcSubs(st *site) int {
	// The stream is registered once the handler starts; poll through the dashboard's count.
	_, body := st.get("Seer", "/c/1/play")
	if strings.Contains(body, "</i> 0 connected") {
		return 0
	}
	return 1
}

func TestCreationWizard(t *testing.T) {
	st, _ := newSite(t)
	ok := func(code int, flash string) {
		t.Helper()
		if code != http.StatusSeeOther || flash != "" {
			t.Fatalf("post: %d %q", code, flash)
		}
	}
	ok(st.post("Ana", "/agents", url.Values{"name": {"Ines"}, "class": {"prowler"}, "campaign_id": {"0"}}))

	// Bram can't touch Ana's Agent or its wizard.
	if code, _ := st.get("Bram", "/agents/1/wizard"); code != http.StatusForbidden && code != http.StatusNotFound {
		t.Errorf("another player opened the wizard: %d", code)
	}

	// Named on the quick form already, so the wizard skips its Name step.
	if code, body := st.get("Ana", "/agents/1/wizard"); code != http.StatusOK || !strings.Contains(body, "As a child") {
		t.Fatalf("wizard should start at the childhood step: %d", code)
	}

	ok(st.post("Ana", "/agents/1", url.Values{"set.child_phrase": {"Never looking back"}, "set.child_card": {"The Fool"}, "back": {"/agents/1/wizard?step=2"}}))
	if _, body := st.get("Ana", "/agents/1/wizard"); !strings.Contains(body, "survived") {
		t.Fatal("should auto-advance to step 2")
	}
	ok(st.post("Ana", "/agents/1", url.Values{"set.adult_phrase": {"My wild youth"}, "set.adult_card": {"The Chariot"}, "set.adult_verb": {"survived"}, "back": {"/agents/1/wizard?step=3"}}))
	ok(st.post("Ana", "/agents/1", url.Values{"set.burden": {"Reckless"}, "set.burden_card": {"The Chariot"}, "back": {"/agents/1/wizard?step=4"}}))
	ok(st.post("Ana", "/agents/1", url.Values{"set.ideal": {"Curious"}, "set.ideal_card": {"The Fool"}, "back": {"/agents/1/wizard?step=5"}}))

	if _, body := st.get("Ana", "/agents/1/wizard"); !strings.Contains(body, "Abilities") || !strings.Contains(body, "WISP") {
		t.Fatal("should be on step 5 with the class ability list")
	}
	ok(st.post("Ana", "/agents/1", url.Values{"ability.add": {"wisp"}, "back": {"/agents/1/wizard?step=5"}}))
	if _, body := st.get("Ana", "/agents/1/wizard"); strings.Contains(body, `value="wisp"`) {
		t.Error("an already-chosen ability shouldn't be offered again")
	}
	ok(st.post("Ana", "/agents/1", url.Values{"ability.add": {"burglar"}, "back": {"/agents/1/wizard?step=5"}}))
	if _, body := st.get("Ana", "/agents/1/wizard"); !strings.Contains(body, "Skills") {
		t.Fatal("should auto-advance to step 6 once 2 abilities are chosen")
	}

	// Prowler pre-fills Slip 2, Finesse 1 (3 points); add 4 more (7 total, cap 2/skill).
	ok(st.post("Ana", "/agents/1", url.Values{"skill.Skirmish": {"2"}, "skill.Convince": {"2"}, "back": {"/agents/1/wizard?step=6"}}))
	if _, body := st.get("Ana", "/agents/1/wizard?step=skills"); !strings.Contains(body, "7 of 7 points used") {
		t.Fatal("7 points should satisfy the skill step")
	}
	// Prowler has no magic: should skip straight to the look step.
	if _, body := st.get("Ana", "/agents/1/wizard"); !strings.Contains(body, "Look, age and culture") {
		t.Fatal("non-magical class should skip the magic step")
	}
	ok(st.post("Ana", "/agents/1", url.Values{"set.name": {"Ines"}, "set.age": {"24"}, "set.culture": {"Lisbon"}, "set.look": {"Sharp-eyed"}, "back": {"/agents/1/wizard?step=9"}}))
	ok(st.post("Ana", "/agents/1", url.Values{"set.why": {"Fleeing famine, disaster or war"}, "back": {"/agents/1/wizard?step=10"}}))

	ok(st.post("Ana", "/agents/1/contacts", url.Values{"set.kind": {"Homeland"}, "set.name": {"Mother Agnese"}, "set.card": {"Page of Cups"}, "set.affection": {"4"}, "back": {"/agents/1/wizard?step=10"}}))
	if _, body := st.get("Ana", "/agents/1/wizard"); !strings.Contains(body, "Dioscorian contact") {
		t.Fatal("should auto-advance to step 11 once the homeland contact exists")
	}
	ok(st.post("Ana", "/agents/1/contacts", url.Values{"set.kind": {"Dioscorian"}, "set.name": {"Old Marco"}, "set.affection": {"1"}, "back": {"/agents/1/wizard?step=11"}}))

	if _, body := st.get("Ana", "/agents/1/wizard"); !strings.Contains(body, "is ready") || !strings.Contains(body, "Mother Agnese") {
		t.Fatal("should finish on the review step with a transcription checklist")
	}
}

func TestWizardMagicStepAndDraw(t *testing.T) {
	st, _ := newSite(t)
	if code, _ := st.post("Ana", "/agents", url.Values{"name": {"Cyrus"}, "class": {"occultist"}, "campaign_id": {"0"}}); code != http.StatusSeeOther {
		t.Fatal("create agent")
	}
	// Fast-forward past steps 1-6 to reach the magic step.
	form := func(v url.Values) {
		if code, flash := st.post("Ana", "/agents/1", v); code != http.StatusSeeOther || flash != "" {
			t.Fatalf("post %v: %d %q", v, code, flash)
		}
	}
	form(url.Values{"set.child_phrase": {"x"}, "set.child_card": {"The Fool"}})
	form(url.Values{"set.adult_phrase": {"x"}, "set.adult_card": {"The Fool"}, "set.adult_verb": {"survived"}})
	form(url.Values{"set.burden": {"Reckless"}})
	form(url.Values{"set.ideal": {"Curious"}})
	form(url.Values{"ability.add": {"evil-eye"}})
	// Occultist creation.Abilities is 2 in the fixture but only 1 ability exists on the class;
	// add a custom one to satisfy the count.
	form(url.Values{"ability.custom_name": {"Extra"}, "ability.custom_text": {"test"}})
	form(url.Values{"skill.Skirmish": {"2"}, "skill.Convince": {"2"}, "skill.Study": {"2"}}) // Unleash1+Channel2 prefilled + 6 = 9, over target but fine for this test

	if _, body := st.get("Ana", "/agents/1/wizard?step=magic"); !strings.Contains(body, "Occultists, Illusionists, Siphoners") {
		t.Fatalf("occultist should see the magic step prompt: %s", body)
	}
	form(url.Values{"prof.add": {"Illusion"}, "prof.rank": {"Adept"}})
	if _, body := st.get("Ana", "/agents/1/wizard?step=magic"); !strings.Contains(body, "Illusion") || !strings.Contains(body, "Continue") {
		t.Fatal("proficiency should be listed with a continue link")
	}

	// Digital draw: returns a card and re-renders that step's card picker with its options.
	r := httptest.NewRequest(http.MethodPost, "/agents/1/wizard/draw", strings.NewReader(url.Values{"for": {"burden"}}.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	r.RemoteAddr = "192.168.1.10:1"
	r.AddCookie(st.cookies["Ana"])
	w := httptest.NewRecorder()
	st.h.ServeHTTP(w, r)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `id="card-burden"`) || !strings.Contains(w.Body.String(), "TestBurden") {
		t.Errorf("digital draw: %d %s", w.Code, w.Body.String())
	}
}

func TestDowntimeSubmitApproveFlow(t *testing.T) {
	st, svc := newSite(t)
	ok := func(code int, flash string) {
		t.Helper()
		if code != http.StatusSeeOther || flash != "" {
			t.Fatalf("post: %d %q", code, flash)
		}
	}
	ok(st.post("Seer", "/campaigns", url.Values{"name": {"Venice"}, "mode": {"group"}}))
	ok(st.post("Seer", "/c/1/members", url.Values{"user_id": {"2"}, "member": {"on"}}))
	ok(st.post("Ana", "/c/1/agents", url.Values{"name": {"Ines"}, "class": {"prowler"}}))
	ok(st.post("Ana", "/agents/1", url.Values{"set.vices": {"Gambling"}}))
	ok(st.post("Ana", "/agents/1/contacts", url.Values{"set.kind": {"Homeland"}, "set.name": {"Mother Agnese"}, "set.affection": {"6"}}))

	// A non-owner can't open or submit downtime for Ines.
	if code, _ := st.get("Bram", "/agents/1/downtime"); code != http.StatusForbidden && code != http.StatusNotFound {
		t.Errorf("another player opened the downtime page: %d", code)
	}
	if code, body := st.get("Ana", "/agents/1/downtime"); code != http.StatusOK || !strings.Contains(body, "Gambling") {
		t.Fatalf("downtime page: %d", code)
	}
	// Players can't reach the Seer's queue.
	if code, _ := st.get("Ana", "/c/1/downtime"); code != http.StatusForbidden {
		t.Errorf("player opened the downtime queue: %d", code)
	}

	form := url.Values{
		"vice0.suit":   {"Cups"},
		"action0.kind": {"heal"}, "action0.harm_type": {"P"}, "action0.amount": {"2"}, "action0.suit": {"Swords"},
		"action1.kind": {"reflect"}, "action1.trait": {"ideal"}, "action1.delta": {"1"},
		"player_note": {"a quiet week"},
	}
	if code, flash := st.post("Ana", "/agents/1/downtime", form); code != http.StatusSeeOther || flash != "" {
		t.Fatalf("submit: %d %q", code, flash)
	}

	// The submission shows on the player's own downtime page and the Seer's queue, but not to Bram.
	if _, body := st.get("Ana", "/agents/1/downtime"); !strings.Contains(body, "pending") || !strings.Contains(body, "a quiet week") {
		t.Error("submission should show on Ana's downtime page")
	}
	_, queue := st.get("Seer", "/c/1/downtime")
	if !strings.Contains(queue, "Ines") || !strings.Contains(queue, "Gambling") || !strings.Contains(queue, "reflect") {
		t.Fatalf("Seer's queue: %s", queue)
	}

	var seer db.User
	svc.DB.First(&seer, 1)
	pending, err := svc.DowntimeSubmissions(campaign.Actor{User: &seer}, 1, campaign.DowntimeStatusPending)
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending: %v %v", pending, err)
	}
	subID := pending[0].ID

	// Approve it and check the sheet actually changed.
	ok(st.post("Seer", fmt.Sprintf("/downtime/%d/approve", subID), url.Values{"note": {"looks good"}, "back": {"/c/1/downtime"}}))
	ag, _ := svc.Agent(campaign.Actor{User: &seer}, 1)
	if ag.IdealTrack != 1 {
		t.Errorf("reflect should have moved the ideal track: %d", ag.IdealTrack)
	}
	if len(ag.Harm["Cups"]) == 0 { // vice harm landed somewhere in Cups
		t.Errorf("vice harm missing: %+v", ag.Harm)
	}
	_, queue2 := st.get("Seer", "/c/1/downtime")
	if !strings.Contains(queue2, "approved") {
		t.Errorf("decided list should show the approved submission: %s", queue2)
	}

	// A second submission, rejected this time.
	ok(st.post("Ana", "/agents/1/downtime", url.Values{"vice0.suit": {"Cups"}, "action0.kind": {"reflect"}, "action0.trait": {"burden"}, "action0.delta": {"1"}}))
	pending2, _ := svc.DowntimeSubmissions(campaign.Actor{User: &seer}, 1, campaign.DowntimeStatusPending)
	if len(pending2) != 1 {
		t.Fatalf("expected 1 new pending submission, got %d", len(pending2))
	}
	ok(st.post("Seer", fmt.Sprintf("/downtime/%d/reject", pending2[0].ID), url.Values{"reason": {"too much harm this week"}, "back": {"/c/1/downtime"}}))
	ag2, _ := svc.Agent(campaign.Actor{User: &seer}, 1)
	if ag2.BurdenTrack != 0 {
		t.Error("a rejected submission must not touch the sheet")
	}
	if _, body := st.get("Ana", "/agents/1/downtime"); !strings.Contains(body, "rejected") || !strings.Contains(body, "too much harm this week") {
		t.Error("the player should see the rejection reason")
	}
}
