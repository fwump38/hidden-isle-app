package assist

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/fwump38/hidden-isle-app/internal/campaign"
	"github.com/fwump38/hidden-isle-app/internal/db"
	"github.com/fwump38/hidden-isle-app/internal/gamedata"
	"github.com/fwump38/hidden-isle-app/internal/rules"
)

func fixtureSnap() *gamedata.Snapshot {
	s := &gamedata.Snapshot{}
	s.Skills.Skills = []gamedata.Skill{{Name: "Skirmish", Suit: "Swords"}}
	s.Classes.Classes = []gamedata.Class{{ID: "hunter", Name: "Hunter", PrefilledSkills: map[string]int{"Skirmish": 1}}}
	s.Campaign = gamedata.Campaign{AgentStatus: []string{"Active", "Dead"}, CampaignMode: []string{"group"}}
	s.Limits.Agent.BurdenTrack = gamedata.Range{Min: 0, Max: 10}
	s.Limits.Agent.IdealTrack = gamedata.Range{Min: 0, Max: 10}
	s.Limits.Agent.SuitXP = gamedata.Range{Min: 0, Max: 7}
	s.Limits.Agent.AbilityXP = gamedata.Range{Min: 0, Max: 7}
	s.Limits.Agent.LoadUsed = gamedata.Range{Min: 0, Max: 10}
	s.Limits.Agent.Skill.Max, s.Limits.Agent.Skill.MaxUnlocked = 3, 4
	s.Limits.Agent.HarmPerSuit.Max = 2
	s.Limits.Agent.HarmPerSuit.Types = []string{"P", "S", "T"}
	s.Limits.Agent.Virtues.Max = 3
	s.Limits.Agent.ProficiencyClock.Segments = 6
	s.Limits.Agent.ProficiencyClock.Ranks = []string{"Novice", "Adept", "Master"}
	return s
}

type world struct {
	svc    *campaign.Service
	assist *Service
	seer   *db.User
	ana    *db.User
	agent  *db.Agent
	camp   *db.Campaign
	calls  *int32
}

func setup(t *testing.T, handler http.HandlerFunc) *world {
	t.Helper()
	g, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	svc := campaign.New(g, gamedata.StaticStore(fixtureSnap()))

	seer := &db.User{Name: "Seer", Role: db.RoleSeer, Active: true}
	if err := g.Create(seer).Error; err != nil {
		t.Fatal(err)
	}
	ana := &db.User{Name: "Ana", Role: db.RolePlayer, Active: true}
	if err := g.Create(ana).Error; err != nil {
		t.Fatal(err)
	}
	seerActor := campaign.Actor{User: seer, Via: "web"}
	camp := &db.Campaign{Name: "Test"}
	if err := svc.CreateCampaign(seerActor, camp, campaign.Opts{}); err != nil {
		t.Fatal(err)
	}
	if err := svc.SetMember(seerActor, camp.ID, ana.ID, true); err != nil {
		t.Fatal(err)
	}
	anaActor := campaign.Actor{User: ana, Via: "web"}
	agent, err := svc.NewAgent(anaActor, camp.ID, "Ana's Agent", "hunter", &ana.ID, campaign.Opts{})
	if err != nil {
		t.Fatal(err)
	}

	idx, err := rules.New(g)
	if err != nil {
		t.Fatal(err)
	}

	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		handler(w, r)
	}))
	t.Cleanup(srv.Close)

	c := New(g, svc, nil, idx, Config{APIKey: "test", BaseURL: srv.URL})
	return &world{svc: svc, assist: c, seer: seer, ana: ana, agent: agent, camp: camp, calls: &calls}
}

func toolUseResp(id, name string, input map[string]any) []byte {
	b, _ := json.Marshal(map[string]any{
		"id": "msg_1", "type": "message", "role": "assistant", "model": "claude-sonnet-5",
		"content":     []map[string]any{{"type": "tool_use", "id": id, "name": name, "input": input}},
		"stop_reason": "tool_use",
		"usage":       map[string]any{"input_tokens": 200, "output_tokens": 30},
	})
	return b
}

// TestSuggestForcesTheToolAndParsesOptions covers the wizard's background call: one request that
// forces offer_suggestions, sends the already-shown options so "more" gives new ones, drops
// options missing a field, stores no chat messages, and counts against the monthly budget.
func TestSuggestForcesTheToolAndParsesOptions(t *testing.T) {
	var body map[string]any
	w := setup(t, func(rw http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&body)
		rw.Header().Set("Content-Type", "application/json")
		rw.Write(toolUseResp("toolu_1", "offer_suggestions", map[string]any{"options": []map[string]any{
			{"word": "Reckless", "reason": "The Chariot charges ahead"},
			{"word": "Proud", "reason": "Too sure of the road"},
			{"reason": "missing its word"},
		}}))
	})
	got, err := w.assist.Suggest(context.Background(), w.ana, SuggestRequest{
		Kind: "burden", Context: []string{"Burden card: The Chariot"}, Hint: "someone who never backs down", Exclude: []string{"Hasty"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Fields["word"] != "Reckless" || got[1].Why != "Too sure of the road" {
		t.Fatalf("suggestions = %+v", got)
	}
	tc, _ := body["tool_choice"].(map[string]any)
	if tc["type"] != "tool" || tc["name"] != "offer_suggestions" {
		t.Errorf("tool_choice = %v, want the forced offer_suggestions tool", body["tool_choice"])
	}
	raw, _ := json.Marshal(body["messages"])
	for _, want := range []string{"The Chariot", "never backs down", "Already shown: Hasty"} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("request messages missing %q: %s", want, raw)
		}
	}
	var usage db.AIUsage
	if err := w.svc.DB.Where("user_id = ?", w.ana.ID).First(&usage).Error; err != nil || usage.CostUSD <= 0 {
		t.Errorf("suggestion spend wasn't recorded: %+v %v", usage, err)
	}
	if _, err := w.assist.Suggest(context.Background(), w.ana, SuggestRequest{Kind: "nonsense"}); err == nil {
		t.Error("an unknown kind should be refused without an API call")
	}
}

// TestSuggestWhyKindFieldDoesntCollideWithReason is a regression test: the "why" kind's own field
// used to be named "why" too (p. 41's "why Dioscoria"), which first collided with the rationale
// property (also named "why" at the time) and produced two identical entries in the tool's
// "required" array — an invalid JSON schema (draft 2020-12 requires "required" to be unique),
// rejected by the API with a 400 before the model ever ran. Renaming the rationale to "reason"
// fixed the schema, but the field being named "why" — a near-synonym of "reason" — still reliably
// got the model to swap the two in its actual answers, so the field is now named "sentence".
func TestSuggestWhyKindFieldDoesntCollideWithReason(t *testing.T) {
	var body map[string]any
	w := setup(t, func(rw http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&body)
		rw.Header().Set("Content-Type", "application/json")
		rw.Write(toolUseResp("toolu_1", "offer_suggestions", map[string]any{"options": []map[string]any{
			{"sentence": "Fleeing famine, disaster or war", "reason": "fits a refugee concept"},
		}}))
	})
	got, err := w.assist.Suggest(context.Background(), w.ana, SuggestRequest{Kind: "why"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Fields["sentence"] != "Fleeing famine, disaster or war" || got[0].Why != "fits a refugee concept" {
		t.Fatalf("suggestions = %+v", got)
	}
	tools, _ := body["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("tools = %+v", body["tools"])
	}
	schema, _ := tools[0].(map[string]any)["input_schema"].(map[string]any)
	opts, _ := schema["properties"].(map[string]any)["options"].(map[string]any)
	req, _ := opts["items"].(map[string]any)["required"].([]any)
	seen := map[any]bool{}
	for _, r := range req {
		if seen[r] {
			t.Fatalf("required lists %v more than once — invalid JSON schema: %v", r, req)
		}
		seen[r] = true
	}
}

// TestSuggestSeerKindUsesSeerInstructionsAndBrief covers the Seer's own suggestion boxes
// (adversary, session, clock, territory_event, handout): the Seer instructions are sent, not the
// player ones, and the brief's lines reach the request alongside any Context.
func TestSuggestSeerKindUsesSeerInstructionsAndBrief(t *testing.T) {
	var body map[string]any
	w := setup(t, func(rw http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&body)
		rw.Header().Set("Content-Type", "application/json")
		rw.Write(toolUseResp("toolu_1", "offer_suggestions", map[string]any{"options": []map[string]any{
			{"name": "The Choir", "leader": "Brother Anselm", "plot": "smuggling", "motivation": "profit", "members": "Brother Anselm — greedy", "reason": "fits the docks"},
		}}))
	})
	brief := &Brief{}
	brief.Add("Territory: The Harbor, a smuggler's den")
	got, err := w.assist.Suggest(context.Background(), w.ana, SuggestRequest{
		Kind: "adversary", Brief: brief, Hint: "something tied to the docks",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Fields["name"] != "The Choir" {
		t.Fatalf("suggestions = %+v", got)
	}
	raw, _ := json.Marshal(body["system"])
	if !strings.Contains(string(raw), "help the Seer") {
		t.Errorf("system prompt should use the Seer instructions: %s", raw)
	}
	raw, _ = json.Marshal(body["messages"])
	for _, want := range []string{"The Harbor", "smuggler's den", "tied to the docks"} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("request messages missing %q: %s", want, raw)
		}
	}
}

func TestSuggestRespectsTheBudget(t *testing.T) {
	w := setup(t, func(rw http.ResponseWriter, r *http.Request) {
		t.Error("no API call should be made over the cap")
	})
	w.assist.cfg.PlayerCapUSD = 1
	w.svc.DB.Create(&db.AIUsage{UserID: w.ana.ID, Month: currentMonth(), CostUSD: 2})
	if _, err := w.assist.Suggest(context.Background(), w.ana, SuggestRequest{Kind: "name"}); !errors.Is(err, ErrBudget) {
		t.Fatalf("err = %v, want ErrBudget", err)
	}
}

func textToolResp(text string) []byte {
	b, _ := json.Marshal(map[string]any{
		"id": "msg_1", "type": "message", "role": "assistant", "model": "claude-sonnet-5",
		"content":     []map[string]any{{"type": "tool_use", "id": "toolu_1", "name": "offer_text", "input": map[string]any{"text": text}}},
		"stop_reason": "tool_use",
		"usage":       map[string]any{"input_tokens": 150, "output_tokens": 40},
	})
	return b
}

// TestWriteEnhanceForcesTheToolAndSendsTheBrief covers Enhance: the request carries both the
// author's draft and the brief's lines, forces offer_text, and spends against the same budget.
func TestWriteEnhanceForcesTheToolAndSendsTheBrief(t *testing.T) {
	var body map[string]any
	w := setup(t, func(rw http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&body)
		rw.Header().Set("Content-Type", "application/json")
		rw.Write(textToolResp("A fuller telling of the same events."))
	})
	brief := &Brief{}
	brief.Add("Adversary: The Choir — a smuggling ring")
	got, err := w.assist.Write(context.Background(), w.ana, WriteRequest{
		Field: "journal entry", Mode: ModeEnhance, Text: "We found the smugglers.", Brief: brief,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got != "A fuller telling of the same events." {
		t.Errorf("got = %q", got)
	}
	tc, _ := body["tool_choice"].(map[string]any)
	if tc["type"] != "tool" || tc["name"] != "offer_text" {
		t.Errorf("tool_choice = %v, want the forced offer_text tool", body["tool_choice"])
	}
	raw, _ := json.Marshal(body["messages"])
	for _, want := range []string{"We found the smugglers.", "The Choir"} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("request messages missing %q: %s", want, raw)
		}
	}
	var usage db.AIUsage
	if err := w.svc.DB.Where("user_id = ?", w.ana.ID).First(&usage).Error; err != nil || usage.CostUSD <= 0 {
		t.Errorf("write spend wasn't recorded: %+v %v", usage, err)
	}
}

func TestWriteEnhanceRequiresText(t *testing.T) {
	w := setup(t, func(rw http.ResponseWriter, r *http.Request) {
		t.Error("no API call should be made with nothing to enhance")
	})
	if _, err := w.assist.Write(context.Background(), w.ana, WriteRequest{Field: "journal entry", Mode: ModeEnhance}); err == nil {
		t.Error("expected an error enhancing empty text")
	}
}

func TestWriteDraftNeedsNoText(t *testing.T) {
	var body map[string]any
	w := setup(t, func(rw http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&body)
		rw.Header().Set("Content-Type", "application/json")
		rw.Write(textToolResp("A recap drafted from the log."))
	})
	brief := &Brief{}
	brief.Add("Session: The Choir's Warehouse — the Hand broke the smuggling ring")
	got, err := w.assist.Write(context.Background(), w.ana, WriteRequest{Field: "recap", Mode: ModeDraft, Brief: brief})
	if err != nil {
		t.Fatal(err)
	}
	if got != "A recap drafted from the log." {
		t.Errorf("got = %q", got)
	}
	raw, _ := json.Marshal(body["messages"])
	if strings.Contains(string(raw), "What's written so far") {
		t.Errorf("draft mode shouldn't claim there's existing text: %s", raw)
	}
}

func TestWriteRespectsTheBudget(t *testing.T) {
	w := setup(t, func(rw http.ResponseWriter, r *http.Request) {
		t.Error("no API call should be made over the cap")
	})
	w.assist.cfg.PlayerCapUSD = 1
	w.svc.DB.Create(&db.AIUsage{UserID: w.ana.ID, Month: currentMonth(), CostUSD: 2})
	if _, err := w.assist.Write(context.Background(), w.ana, WriteRequest{Field: "journal entry", Mode: ModeEnhance, Text: "hi"}); !errors.Is(err, ErrBudget) {
		t.Fatalf("err = %v, want ErrBudget", err)
	}
}
