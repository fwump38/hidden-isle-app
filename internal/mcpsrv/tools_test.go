package mcpsrv

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/fwump38/hidden-isle-app/internal/campaign"
	"github.com/fwump38/hidden-isle-app/internal/config"
	"github.com/fwump38/hidden-isle-app/internal/db"
	"github.com/fwump38/hidden-isle-app/internal/gamedata"
	"github.com/fwump38/hidden-isle-app/internal/rules"
)

type harness struct {
	t   *testing.T
	cs  *mcp.ClientSession
	srv *Server
}

func newHarness(t *testing.T, user db.User) *harness {
	t.Helper()
	g, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	snap := gamedata.Fixture()
	var idx *rules.Index
	if src := os.Getenv("HI_TEST_GAMEDATA"); src != "" {
		if snap, err = gamedata.Load(src, "test"); err != nil {
			t.Fatal(err)
		}
		if idx, err = rules.New(g); err != nil {
			t.Fatal(err)
		}
		if err := idx.Build(snap); err != nil {
			t.Fatal(err)
		}
	}
	data := gamedata.StaticStore(snap)
	for _, u := range []db.User{{Name: "Seer", Role: db.RoleSeer, Active: true}, {Name: "Ana", Role: db.RolePlayer, Active: true}} {
		if err := g.Create(&u).Error; err != nil {
			t.Fatal(err)
		}
	}
	srv := New(g, &config.Config{}, data, campaign.New(g, data), idx, "test")
	g.Where("name = ?", user.Name).First(&user)
	srv.testUser = &user
	st, ct := mcp.NewInMemoryTransports()
	ctx := context.Background()
	if _, err := srv.mcp.Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return &harness{t: t, cs: cs, srv: srv}
}

// call runs a tool and decodes its structured result; it fails the test on a tool error.
func (h *harness) call(name string, args map[string]any) map[string]any {
	h.t.Helper()
	out, errText := h.try(name, args)
	if errText != "" {
		h.t.Fatalf("%s: %s", name, errText)
	}
	return out
}

func (h *harness) try(name string, args map[string]any) (map[string]any, string) {
	h.t.Helper()
	res, err := h.cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		h.t.Fatalf("%s: %v", name, err)
	}
	if res.IsError {
		var msg []string
		for _, c := range res.Content {
			if tc, ok := c.(*mcp.TextContent); ok {
				msg = append(msg, tc.Text)
			}
		}
		return nil, strings.Join(msg, " ")
	}
	var out map[string]any
	b, _ := json.Marshal(res.StructuredContent)
	json.Unmarshal(b, &out)
	return out, ""
}

func TestToolsPlayACampaign(t *testing.T) {
	h := newHarness(t, db.User{Name: "Seer"})
	tools, err := h.cs.ListTools(context.Background(), nil)
	if err != nil || len(tools.Tools) < 25 {
		t.Fatalf("tools: %d %v", len(tools.Tools), err)
	}

	c := h.call("create_campaign", map[string]any{"name": "Venice Nights"})
	cid := c["id"]
	ag := h.call("create_agent", map[string]any{"campaign_id": cid, "name": "Ines", "class": "Prowler", "player": "ana"})
	aid := ag["id"]
	h.call("update_record", map[string]any{"kind": "agent", "id": aid, "fields": map[string]any{"burden": "Reckless", "burden_track": 2}, "reason": "creation"})
	h.call("add_harm", map[string]any{"agent_id": aid, "suit": "cups", "type": "P", "amount": 2, "reason": "fell off a gondola"})
	if _, e := h.try("add_harm", map[string]any{"agent_id": aid, "suit": "cups", "type": "S", "reason": "x"}); !strings.Contains(e, "room in") {
		t.Errorf("full suit: %q", e)
	}
	if _, e := h.try("update_record", map[string]any{"kind": "agent", "id": aid, "fields": map[string]any{"burden_track": 9}, "reason": "x"}); !strings.Contains(e, "burden track") {
		t.Errorf("limits must apply over MCP: %q", e)
	}
	xp := h.call("award_xp", map[string]any{"agent_id": aid, "track": "cups", "amount": 7, "reason": "wrap-up"})
	if !strings.Contains(xp["note"].(string), "Track full") {
		t.Errorf("xp note: %v", xp)
	}
	clock := h.call("create_record", map[string]any{"campaign_id": cid, "kind": "clock", "fields": map[string]any{"name": "The guards arrive", "segments": 4}, "reason": "prep"})
	h.call("tick_clock", map[string]any{"clock_id": clock["id"], "amount": 2, "reason": "noisy break-in"})
	h.call("create_record", map[string]any{"campaign_id": cid, "kind": "contact", "fields": map[string]any{"agent_id": aid, "name": "Mother Agnese", "kind": "Homeland", "affection": 4}, "reason": "creation"})

	state := h.call("get_campaign", map[string]any{"campaign_id": cid})
	if players := state["players"].([]any); len(players) != 1 || players[0] != "Ana" {
		t.Errorf("naming a player should add them: %v", state["players"])
	}
	sheet := h.call("get_agent", map[string]any{"agent_id": aid})
	if a := sheet["agent"].(map[string]any); a["burden"] != "Reckless" {
		t.Errorf("sheet: %v", a)
	}

	log := h.call("get_log", map[string]any{"campaign_id": cid, "entity_type": "clock"})
	evs := log["items"].([]any)
	last := evs[0].(map[string]any)
	if !strings.Contains(last["who"].(string), "(Claude)") || last["reason"] != "noisy break-in" {
		t.Errorf("log should record Claude and the reason: %v", last)
	}
	h.call("undo_change", map[string]any{"event_id": last["id"], "reason": "wrong clock"})
	cl := h.call("get_record", map[string]any{"kind": "clock", "id": clock["id"]})
	if cl["filled"].(float64) != 0 {
		t.Errorf("undo didn't restore the clock: %v", cl["filled"])
	}
	h.call("write_entry", map[string]any{"campaign_id": cid, "kind": "recap", "title": "Session 1", "body": "We arrived.", "visibility": "party", "published": true})
	if es := h.call("list_entries", map[string]any{"campaign_id": cid})["items"].([]any); len(es) != 1 {
		t.Errorf("entries: %d", len(es))
	}
	if _, e := h.try("delete_record", map[string]any{"kind": "campaign", "id": cid, "reason": "x"}); e == "" {
		t.Error("campaign deletion over MCP should be refused")
	}
	cnt := h.call("challenge_count", map[string]any{"agent_id": aid, "skill": "Slip", "difficulty": "hard", "burden": true})
	if cnt["agent_cards"].(float64) != 3 || cnt["seer_cards"].(float64) != 4 || cnt["mark_burden"] != true {
		t.Errorf("challenge_count (1 + Slip 2 + burden - 1 for 2 Cups harm = 3): %v", cnt)
	}
	res := h.call("challenge_resolve", map[string]any{"skill": "Convince", "played": "5 of Pentacles", "seer": "8 of Swords",
		"fortunes": []map[string]any{{"card": "2 of Swords", "mode": "suit"}, {"card": "3 of Wands", "mode": "add"}}})
	if res["outcome"] != "complicated" {
		t.Errorf("p. 19 example over MCP: %v", res)
	}
	fate := h.call("fate_question", map[string]any{"kind": "closed", "likelihood": "50-50", "yes": []string{"4 of Cups"}, "no": []string{"9 of Cups"}})
	if fate["answer"] != "No" || fate["extreme"] != true {
		t.Errorf("fate_question: %v", fate)
	}
	if num := h.call("fate_question", map[string]any{"kind": "numeric", "range": "2-20", "card": "9 of Wands"}); num["number"].(float64) != 18 {
		t.Errorf("numeric: %v", num)
	}
	if ev := h.call("random_event", map[string]any{"card": "Ace of Swords"}); ev["when"] != "future" {
		t.Errorf("random_event: %v", ev)
	}
	card, e := h.try("lookup_card", map[string]any{"name": "Page of Cups"})
	if e != "" || card["name"] != "Page of Cups" {
		t.Errorf("lookup_card: %v %s", card, e)
	}

	if os.Getenv("HI_TEST_GAMEDATA") != "" {
		hits := h.call("search_rules", map[string]any{"query": "harm suit fewer card"})["items"].([]any)
		if len(hits) == 0 {
			t.Fatal("search found nothing")
		}
		ref := hits[0].(map[string]any)["ref"]
		if secs := h.call("read_rules", map[string]any{"ref": ref})["items"].([]any); len(secs) != 1 {
			t.Errorf("read_rules: %v", secs)
		}

		// The plugin's skills are served as prompts.
		ps, err := h.cs.ListPrompts(context.Background(), nil)
		if err != nil || len(ps.Prompts) < 10 {
			t.Fatalf("prompts: %v %v", len(ps.Prompts), err)
		}
		got, err := h.cs.GetPrompt(context.Background(), &mcp.GetPromptParams{Name: "challenge", Arguments: map[string]string{"request": "Ines picks the lock"}})
		if err != nil {
			t.Fatal(err)
		}
		text := got.Messages[0].Content.(*mcp.TextContent).Text
		if !strings.Contains(text, "# Run a challenge") || !strings.Contains(text, "Ines picks the lock") || strings.Contains(strings.ToLower(text), "notion") {
			t.Errorf("challenge prompt text: %.300s", text)
		}
	}
}

func TestToolsRefusePlayers(t *testing.T) {
	h := newHarness(t, db.User{Name: "Ana"})
	if _, e := h.try("list_campaigns", nil); !strings.Contains(e, "only the Seer") {
		t.Errorf("player used MCP: %q", e)
	}
}

func TestWidgets(t *testing.T) {
	h := newHarness(t, db.User{Name: "Seer"})
	ctx := context.Background()

	// Each show_* tool names a ui:// resource that serves a self-contained MCP Apps page.
	tools, err := h.cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	uis := map[string]string{}
	for _, tl := range tools.Tools {
		if ui, ok := tl.Meta["ui"].(map[string]any); ok {
			uis[tl.Name] = ui["resourceUri"].(string)
		}
	}
	if len(uis) != 3 || uis["show_agent"] != "ui://hidden-isle/agent-sheet.html" {
		t.Fatalf("ui tools: %v", uis)
	}
	for name, uri := range uis {
		res, err := h.cs.ReadResource(ctx, &mcp.ReadResourceParams{URI: uri})
		if err != nil {
			t.Fatalf("%s: %v", uri, err)
		}
		c := res.Contents[0]
		if c.MIMEType != "text/html;profile=mcp-app" || !strings.Contains(c.Text, "ui/initialize") ||
			strings.Contains(c.Text, "SHARED_") || !strings.Contains(c.Text, `"`+name+`"`) {
			t.Errorf("%s: mime %q, shared code inlined? %.200s", uri, c.MIMEType, c.Text)
		}
	}

	cid := h.call("create_campaign", map[string]any{"name": "Venice Nights"})["id"]
	aid := h.call("create_agent", map[string]any{"campaign_id": cid, "name": "Ines", "class": "Prowler", "player": "ana"})["id"]
	h.call("add_harm", map[string]any{"agent_id": aid, "suit": "cups", "type": "S", "reason": "x"})
	h.call("create_record", map[string]any{"campaign_id": cid, "kind": "clock", "fields": map[string]any{"name": "Her debt comes due", "segments": 4, "agent_id": aid}, "reason": "x"})
	h.call("create_record", map[string]any{"campaign_id": cid, "kind": "clock", "fields": map[string]any{"name": "The Doge learns", "segments": 6, "visibility": "seer"}, "reason": "x"})

	sheet := h.call("show_agent", map[string]any{"agent_id": aid})
	suits := sheet["suits"].([]any)
	cups := suits[2].(map[string]any)
	if len(suits) != 4 || cups["suit"] != "Cups" || cups["harm"].([]any)[0] != "S" || len(cups["skills"].([]any)) != 3 {
		t.Errorf("sheet suits: %v", suits)
	}
	if cl := sheet["clocks"].([]any); len(cl) != 1 {
		t.Errorf("sheet clocks: %v", cl)
	}

	clocks := h.call("show_clocks", map[string]any{"campaign_id": cid})
	if cl := clocks["clocks"].([]any); len(cl) != 2 || cl[0].(map[string]any)["agent_name"] != "Ines" {
		t.Errorf("clocks (the Seer sees hidden ones too): %v", cl)
	}
	if segs := clocks["segment_choices"].([]any); len(segs) == 0 {
		t.Errorf("segment choices: %v", clocks)
	}

	ch := h.call("show_challenge", map[string]any{"agent_id": aid, "skill": "Slip"})
	if ch["skill"] != "Slip" || len(ch["pips"].([]any)) == 0 || ch["ideas"].(map[string]any)["failure"] == nil {
		t.Errorf("challenge view: %v", ch)
	}
	if _, e := h.try("show_challenge", map[string]any{"agent_id": aid, "skill": "Juggle"}); !strings.Contains(e, "unknown skill") {
		t.Errorf("bad skill: %q", e)
	}
}
