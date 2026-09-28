package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	svc   *campaign.Service
	chat  *Service
	seer  *db.User
	ana   *db.User
	agent *db.Agent
	camp  *db.Campaign
	calls *int32
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
	return &world{svc: svc, chat: c, seer: seer, ana: ana, agent: agent, camp: camp, calls: &calls}
}

func textResp(text string) []byte {
	b, _ := json.Marshal(map[string]any{
		"id": "msg_1", "type": "message", "role": "assistant", "model": "claude-sonnet-5",
		"content":     []map[string]any{{"type": "text", "text": text}},
		"stop_reason": "end_turn",
		"usage":       map[string]any{"input_tokens": 100, "output_tokens": 20},
	})
	return b
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

func TestSendCallsToolThenReplies(t *testing.T) {
	step := int32(0)
	w := setup(t, func(rw http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&step, 1)
		rw.Header().Set("Content-Type", "application/json")
		if n == 1 {
			rw.Write(toolUseResp("toolu_1", "get_agent", map[string]any{"agent_id": 0}))
			return
		}
		rw.Write(textResp("Your Agent looks great!"))
	})

	th, err := w.chat.Thread(w.ana.ID, w.camp.ID)
	if err != nil {
		t.Fatal(err)
	}
	// The fake tool_use above references agent_id 0, which won't resolve; swap in the real id
	// by wrapping the handler isn't easy here, so instead just check the loop completes and the
	// final assistant text is stored even though the tool call itself errors.
	reply, err := w.chat.Send(context.Background(), w.ana, th.ID, "How's my Agent?", "")
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if reply.Message.Text != "Your Agent looks great!" {
		t.Errorf("reply = %q", reply.Message.Text)
	}
	if atomic.LoadInt32(w.calls) != 2 {
		t.Errorf("calls = %d, want 2 (one tool round-trip)", *w.calls)
	}
	var msgs []db.ChatMessage
	w.svc.DB.Where("thread_id = ?", th.ID).Order("id").Find(&msgs)
	if len(msgs) != 2 || msgs[0].Role != "user" || msgs[1].Role != "assistant" {
		t.Fatalf("stored messages = %+v", msgs)
	}
	var usage db.ChatUsage
	if err := w.svc.DB.Where("user_id = ?", w.ana.ID).First(&usage).Error; err != nil {
		t.Fatal(err)
	}
	if usage.InputTokens != 300 || usage.OutputTokens != 50 {
		t.Errorf("usage = %+v", usage)
	}
	if usage.CostUSD <= 0 {
		t.Errorf("cost = %v, want > 0", usage.CostUSD)
	}
}

func TestSendRefusesOverBudget(t *testing.T) {
	w := setup(t, func(rw http.ResponseWriter, r *http.Request) {
		rw.Write(textResp("hi"))
	})
	w.chat.cfg.PlayerCapUSD = 0.01
	th, err := w.chat.Thread(w.ana.ID, w.camp.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.svc.DB.Create(&db.ChatUsage{UserID: w.ana.ID, Month: currentMonth(), CostUSD: 1}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := w.chat.Send(context.Background(), w.ana, th.ID, "hi", ""); !errors.Is(err, ErrBudget) {
		t.Errorf("err = %v, want ErrBudget", err)
	}
	if atomic.LoadInt32(w.calls) != 0 {
		t.Errorf("calls = %d, want 0 (should refuse before calling the API)", *w.calls)
	}
}

func TestSuggestAgentChangeApplyAndDismiss(t *testing.T) {
	step := int32(0)
	var agentID uint
	w := setup(t, func(rw http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&step, 1)
		rw.Header().Set("Content-Type", "application/json")
		if n == 1 {
			rw.Write(toolUseResp("toolu_1", "suggest_agent_change", map[string]any{
				"agent_id": agentID, "fields": map[string]any{"look": "a scar over one eye"}, "summary": "set look", "why": "creation help",
			}))
			return
		}
		rw.Write(textResp("Suggested a look for your Agent."))
	})
	agentID = w.agent.ID

	th, err := w.chat.Thread(w.ana.ID, w.camp.ID)
	if err != nil {
		t.Fatal(err)
	}
	reply, err := w.chat.Send(context.Background(), w.ana, th.ID, "Suggest a look for my Agent", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(reply.Suggestions) != 1 {
		t.Fatalf("suggestions = %d, want 1", len(reply.Suggestions))
	}
	sug := reply.Suggestions[0]

	// Someone else can't apply it.
	other := &db.User{Name: "Bram", Role: db.RolePlayer, Active: true}
	if err := w.svc.DB.Create(other).Error; err != nil {
		t.Fatal(err)
	}
	if err := w.chat.ApplySuggestion(campaign.Actor{User: other, Via: "web"}, sug.ID); err == nil {
		t.Fatal("expected forbidden for another user")
	}

	// The Agent isn't touched until Apply.
	var before db.Agent
	w.svc.DB.First(&before, w.agent.ID)
	if before.Look == "a scar over one eye" {
		t.Fatal("Agent changed before Apply")
	}

	if err := w.chat.ApplySuggestion(campaign.Actor{User: w.ana, Via: "web"}, sug.ID); err != nil {
		t.Fatalf("ApplySuggestion: %v", err)
	}
	var after db.Agent
	w.svc.DB.First(&after, w.agent.ID)
	if after.Look != "a scar over one eye" {
		t.Errorf("Look = %q, want the suggested look", after.Look)
	}
	if err := w.chat.ApplySuggestion(campaign.Actor{User: w.ana, Via: "web"}, sug.ID); err == nil {
		t.Fatal("expected an error applying an already-applied suggestion")
	}
}

func TestUpdateMemoryTool(t *testing.T) {
	step := int32(0)
	w := setup(t, func(rw http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&step, 1)
		rw.Header().Set("Content-Type", "application/json")
		if n == 1 {
			rw.Write(toolUseResp("toolu_1", "update_memory", map[string]any{"note": "Ana is playing a Hunter named Vex."}))
			return
		}
		rw.Write(textResp("Got it."))
	})
	th, err := w.chat.Thread(w.ana.ID, w.camp.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.chat.Send(context.Background(), w.ana, th.ID, "Remember my Agent's name is Vex", ""); err != nil {
		t.Fatal(err)
	}
	var reloaded db.ChatThread
	w.svc.DB.First(&reloaded, th.ID)
	if reloaded.Memory != "Ana is playing a Hunter named Vex." {
		t.Errorf("memory = %q", reloaded.Memory)
	}
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
			{"word": "Reckless", "why": "The Chariot charges ahead"},
			{"word": "Proud", "why": "Too sure of the road"},
			{"why": "missing its word"},
		}}))
	})
	got, err := w.chat.Suggest(context.Background(), w.ana, SuggestRequest{
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
	var msgs int64
	w.svc.DB.Model(&db.ChatMessage{}).Count(&msgs)
	if msgs != 0 {
		t.Errorf("suggestions stored %d chat messages; they should never touch a thread", msgs)
	}
	var usage db.ChatUsage
	if err := w.svc.DB.Where("user_id = ?", w.ana.ID).First(&usage).Error; err != nil || usage.CostUSD <= 0 {
		t.Errorf("suggestion spend wasn't recorded: %+v %v", usage, err)
	}
	if _, err := w.chat.Suggest(context.Background(), w.ana, SuggestRequest{Kind: "nonsense"}); err == nil {
		t.Error("an unknown kind should be refused without an API call")
	}
}

func TestSuggestRespectsTheBudget(t *testing.T) {
	w := setup(t, func(rw http.ResponseWriter, r *http.Request) {
		t.Error("no API call should be made over the cap")
	})
	w.chat.cfg.PlayerCapUSD = 1
	w.svc.DB.Create(&db.ChatUsage{UserID: w.ana.ID, Month: currentMonth(), CostUSD: 2})
	if _, err := w.chat.Suggest(context.Background(), w.ana, SuggestRequest{Kind: "name"}); !errors.Is(err, ErrBudget) {
		t.Fatalf("err = %v, want ErrBudget", err)
	}
}

// TestSendTellsTheModelWhichPageIsOpen covers the side panel: the chat sits beside the app, so the
// system prompt names the Agent whose sheet or wizard the player has open.
func TestSendTellsTheModelWhichPageIsOpen(t *testing.T) {
	var body map[string]any
	w := setup(t, func(rw http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&body)
		rw.Header().Set("Content-Type", "application/json")
		rw.Write(textResp("ok"))
	})
	th, err := w.chat.Thread(w.ana.ID, w.camp.ID)
	if err != nil {
		t.Fatal(err)
	}
	page := fmt.Sprintf("/agents/%d/wizard", w.agent.ID)
	if _, err := w.chat.Send(context.Background(), w.ana, th.ID, "what next?", page); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(body["system"])
	if !strings.Contains(string(raw), "creation wizard for Agent") || !strings.Contains(string(raw), "Ana's Agent") {
		t.Errorf("system prompt doesn't describe the open page: %s", raw)
	}
}
