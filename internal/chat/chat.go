// Package chat is the in-app Claude chat for players: character-creation help and rules Q&A,
// on the Seer's own Anthropic API budget. It never touches claude.ai or the Seer's MCP session;
// each request is built fresh from the database (the player's own Agents, party-visible facts,
// and a short memory note), and tools are a fixed, player-scoped, read-mostly allowlist. The one
// write path (suggest_agent_change) only stages a change: nothing touches a sheet until the
// player approves it, which goes through campaign.Service.Update exactly like every other edit.
package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/anthropics/anthropic-sdk-go/packages/param"
	"gorm.io/gorm"

	"github.com/fwump38/hidden-isle-app/internal/campaign"
	"github.com/fwump38/hidden-isle-app/internal/db"
	"github.com/fwump38/hidden-isle-app/internal/gamedata"
	"github.com/fwump38/hidden-isle-app/internal/rules"
)

var (
	ErrBudget = errors.New("the monthly chat budget has been reached; ask the Seer to raise it")
	ErrRate   = errors.New("slow down a little before sending another message")
)

const (
	maxToolTurns    = 6
	maxReplyTokens  = 1024
	historyMessages = 20 // most recent user+assistant turns kept as context
	rateLimitCount  = 10
	rateLimitWindow = 5 * time.Minute
)

// Config is set once from environment variables; see internal/config.
type Config struct {
	APIKey          string
	Model           string
	GlobalCapUSD    float64 // 0 = no cap
	PlayerCapUSD    float64 // 0 = no cap
	PriceInPerMTok  float64
	PriceOutPerMTok float64
	BaseURL         string // tests only
}

type Service struct {
	db     *gorm.DB
	svc    *campaign.Service
	data   *gamedata.Store
	rules  *rules.Index
	cfg    Config
	client anthropic.Client

	mu     sync.Mutex
	recent map[uint][]time.Time
}

func New(g *gorm.DB, svc *campaign.Service, data *gamedata.Store, idx *rules.Index, cfg Config) *Service {
	if cfg.Model == "" {
		cfg.Model = "claude-sonnet-5"
	}
	if cfg.PriceInPerMTok <= 0 {
		cfg.PriceInPerMTok = 3
	}
	if cfg.PriceOutPerMTok <= 0 {
		cfg.PriceOutPerMTok = 15
	}
	opts := []option.RequestOption{option.WithAPIKey(cfg.APIKey)}
	if cfg.BaseURL != "" {
		opts = append(opts, option.WithBaseURL(cfg.BaseURL))
	}
	return &Service{db: g, svc: svc, data: data, rules: idx, cfg: cfg,
		client: anthropic.NewClient(opts...), recent: map[uint][]time.Time{}}
}

// Reply is what one Send call produces.
type Reply struct {
	Message     db.ChatMessage
	Suggestions []db.ChatSuggestion
}

// Thread finds or creates the player's one thread for a campaign (0 = outside any campaign).
func (s *Service) Thread(userID, campaignID uint) (*db.ChatThread, error) {
	var th db.ChatThread
	err := s.db.Where(db.ChatThread{UserID: userID, CampaignID: campaignID}).
		Attrs(db.ChatThread{Title: "Chat"}).FirstOrCreate(&th).Error
	return &th, err
}

// History returns a thread's messages and any suggestions attached to it, oldest first.
func (s *Service) History(threadID uint) ([]db.ChatMessage, []db.ChatSuggestion, error) {
	var msgs []db.ChatMessage
	if err := s.db.Where("thread_id = ?", threadID).Order("id").Find(&msgs).Error; err != nil {
		return nil, nil, err
	}
	var sugs []db.ChatSuggestion
	err := s.db.Where("thread_id = ?", threadID).Order("id").Find(&sugs).Error
	return msgs, sugs, err
}

func (s *Service) checkRate(userID uint) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	var recent []time.Time
	for _, t := range s.recent[userID] {
		if now.Sub(t) < rateLimitWindow {
			recent = append(recent, t)
		}
	}
	if len(recent) >= rateLimitCount {
		return ErrRate
	}
	s.recent[userID] = append(recent, now)
	return nil
}

func currentMonth() string { return time.Now().Format("2006-01") }

func (s *Service) monthSpend(month string, userID uint) (userUSD, globalUSD float64) {
	var row struct{ Total float64 }
	s.db.Model(&db.ChatUsage{}).Where("month = ? AND user_id = ?", month, userID).
		Select("COALESCE(SUM(cost_usd), 0) AS total").Scan(&row)
	userUSD = row.Total
	s.db.Model(&db.ChatUsage{}).Where("month = ?", month).
		Select("COALESCE(SUM(cost_usd), 0) AS total").Scan(&row)
	globalUSD = row.Total
	return
}

func (s *Service) cost(inTok, outTok int64) float64 {
	return float64(inTok)/1e6*s.cfg.PriceInPerMTok + float64(outTok)/1e6*s.cfg.PriceOutPerMTok
}

func (s *Service) recordUsage(month string, userID uint, inTok, outTok int64) {
	usd := s.cost(inTok, outTok)
	var u db.ChatUsage
	if err := s.db.Where(db.ChatUsage{UserID: userID, Month: month}).FirstOrCreate(&u).Error; err != nil {
		slog.Error("chat usage", "err", err)
		return
	}
	err := s.db.Model(&u).Updates(map[string]any{
		"input_tokens":  gorm.Expr("input_tokens + ?", inTok),
		"output_tokens": gorm.Expr("output_tokens + ?", outTok),
		"cost_usd":      gorm.Expr("cost_usd + ?", usd),
	}).Error
	if err != nil {
		slog.Error("chat usage", "err", err)
	}
}

// UsageRow is one user's spend this month, for the admin page.
type UsageRow struct {
	UserID  uint
	Name    string
	CostUSD float64
}

// UsageSummary is this month's spend against both caps, for the admin page.
func (s *Service) UsageSummary() (month string, globalUSD float64, rows []UsageRow) {
	month = currentMonth()
	s.db.Table("chat_usages").
		Select("chat_usages.user_id AS user_id, users.name AS name, chat_usages.cost_usd AS cost_usd").
		Joins("JOIN users ON users.id = chat_usages.user_id").
		Where("chat_usages.month = ?", month).
		Order("chat_usages.cost_usd DESC").
		Scan(&rows)
	for _, r := range rows {
		globalUSD += r.CostUSD
	}
	return
}

// Send appends the player's message, runs the tool loop until the assistant produces a final
// reply, and returns it. It records token spend against the month's cap before spending it,
// so a request over the cap is refused with no API call.
func (s *Service) Send(ctx context.Context, u *db.User, threadID uint, text string) (*Reply, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, errors.New("say something first")
	}
	var th db.ChatThread
	if err := s.db.First(&th, threadID).Error; err != nil {
		return nil, err
	}
	if th.UserID != u.ID {
		return nil, campaign.ErrForbidden
	}
	if err := s.checkRate(u.ID); err != nil {
		return nil, err
	}
	month := currentMonth()
	userUSD, globalUSD := s.monthSpend(month, u.ID)
	if s.cfg.PlayerCapUSD > 0 && userUSD >= s.cfg.PlayerCapUSD {
		return nil, fmt.Errorf("%w (your monthly cap)", ErrBudget)
	}
	if s.cfg.GlobalCapUSD > 0 && globalUSD >= s.cfg.GlobalCapUSD {
		return nil, fmt.Errorf("%w (the table's monthly cap)", ErrBudget)
	}

	a := campaign.Actor{User: u, Via: "chat"}
	msgs := s.history(th.ID)
	if err := s.db.Create(&db.ChatMessage{ThreadID: th.ID, Role: "user", Text: text}).Error; err != nil {
		return nil, err
	}
	msgs = append(msgs, anthropic.NewUserMessage(anthropic.NewTextBlock(text)))

	sys := s.buildSystem(a, th)
	tools := s.toolDefs()
	var suggestions []db.ChatSuggestion
	var toolsUsed []string
	var totalIn, totalOut int64

	for turn := 0; turn < maxToolTurns; turn++ {
		resp, err := s.client.Messages.New(ctx, anthropic.MessageNewParams{
			Model: anthropic.Model(s.cfg.Model), MaxTokens: maxReplyTokens,
			System: sys, Messages: msgs, Tools: tools,
		})
		if err != nil {
			return nil, fmt.Errorf("the assistant is unavailable right now: %w", err)
		}
		totalIn += resp.Usage.InputTokens + resp.Usage.CacheCreationInputTokens + resp.Usage.CacheReadInputTokens
		totalOut += resp.Usage.OutputTokens

		var assistantBlocks []anthropic.ContentBlockParamUnion
		var textOut strings.Builder
		type pendingCall struct {
			id, name string
			input    json.RawMessage
		}
		var calls []pendingCall
		for _, blk := range resp.Content {
			switch blk.Type {
			case "text":
				textOut.WriteString(blk.Text)
				assistantBlocks = append(assistantBlocks, anthropic.NewTextBlock(blk.Text))
			case "tool_use":
				assistantBlocks = append(assistantBlocks, anthropic.NewToolUseBlock(blk.ID, blk.Input, blk.Name))
				calls = append(calls, pendingCall{blk.ID, blk.Name, blk.Input})
			}
		}
		if len(assistantBlocks) > 0 {
			msgs = append(msgs, anthropic.NewAssistantMessage(assistantBlocks...))
		}
		if resp.StopReason != anthropic.StopReasonToolUse || len(calls) == 0 {
			s.recordUsage(month, u.ID, totalIn, totalOut)
			s.db.Model(&th).Update("updated_at", time.Now())
			reply := db.ChatMessage{ThreadID: th.ID, Role: "assistant", Text: strings.TrimSpace(textOut.String()),
				Tools: strings.Join(toolsUsed, ", ")}
			if reply.Text == "" {
				reply.Text = "(no reply)"
			}
			if err := s.db.Create(&reply).Error; err != nil {
				return nil, err
			}
			return &Reply{Message: reply, Suggestions: suggestions}, nil
		}
		var resultBlocks []anthropic.ContentBlockParamUnion
		for _, c := range calls {
			toolsUsed = append(toolsUsed, c.name)
			out, isErr, sug := s.runTool(a, th, c.name, c.input)
			if sug != nil {
				suggestions = append(suggestions, *sug)
			}
			resultBlocks = append(resultBlocks, anthropic.NewToolResultBlock(c.id, out, isErr))
		}
		msgs = append(msgs, anthropic.NewUserMessage(resultBlocks...))
	}
	s.recordUsage(month, u.ID, totalIn, totalOut)
	return nil, errors.New("that took too many steps to answer; try asking more directly")
}

// history loads the thread's recent turns as API messages, oldest first.
func (s *Service) history(threadID uint) []anthropic.MessageParam {
	var rows []db.ChatMessage
	s.db.Where("thread_id = ?", threadID).Order("id desc").Limit(historyMessages).Find(&rows)
	slices.Reverse(rows)
	var out []anthropic.MessageParam
	for _, m := range rows {
		if m.Text == "" {
			continue
		}
		if m.Role == "user" {
			out = append(out, anthropic.NewUserMessage(anthropic.NewTextBlock(m.Text)))
		} else {
			out = append(out, anthropic.NewAssistantMessage(anthropic.NewTextBlock(m.Text)))
		}
	}
	return out
}

const systemInstructions = `You are the Hidden Isle player assistant, built into the campaign app. The Hidden Isle is a tarot RPG of sorcery and adventure set in 1562.

Scope: help with character creation and answer rules questions. Politely decline anything else (this isn't a general assistant).

Rules:
- Cite pages ("p." = Rulebook 1.4, "Sheet p." = Character Sheets 1.3). Quote ability text exactly; rulings depend on its wording.
- Never invent numbers or rules. If search_rules/read_rules/get_class/get_table/lookup_card don't cover something, say so plainly.
- The Seer makes every ruling; if a rules question needs a judgment call, say what the book says and suggest asking the Seer.
- You cannot see anything Seer-only (adventures, session prep, secrets, other players' private notes). Don't guess at what might be there.
- To change the player's own Agent sheet, call suggest_agent_change. That only proposes the change as a card in the chat; nothing is applied until the player taps Apply. Never say a change has been made unless the player tells you they applied it.
- Call update_memory when you learn something worth remembering for next time (a chosen name, class, an open question) — a short note, not a transcript.
- Keep replies short and conversational; this is a chat, not an essay.`

func (s *Service) buildSystem(a campaign.Actor, th db.ChatThread) []anthropic.TextBlockParam {
	blocks := []anthropic.TextBlockParam{{Text: systemInstructions, CacheControl: anthropic.NewCacheControlEphemeralParam()}}
	var b strings.Builder
	fmt.Fprintf(&b, "Player: %s\n", a.User.Name)
	if th.CampaignID != 0 {
		if c, err := s.svc.Campaign(a, th.CampaignID); err == nil {
			fmt.Fprintf(&b, "Campaign: %s (season %d, mode %s)\n", c.Name, c.Season, c.Mode)
		}
		if agents, err := s.svc.Agents(a, th.CampaignID); err == nil {
			b.WriteString("Agents in this campaign (get_agent for full details):\n")
			for _, ag := range agents {
				mine := ""
				if ag.OwnerID != nil && *ag.OwnerID == a.User.ID {
					mine = " — the player's own"
				}
				fmt.Fprintf(&b, "- #%d %s, %s, %s%s\n", ag.ID, ag.Name, ag.Class, ag.Status, mine)
			}
		}
	} else if mine, err := s.svc.MyAgents(a); err == nil && len(mine) > 0 {
		b.WriteString("The player's own Agents (not yet in a campaign):\n")
		for _, ag := range mine {
			fmt.Fprintf(&b, "- #%d %s, %s\n", ag.ID, ag.Name, ag.Class)
		}
	}
	if th.Memory != "" {
		fmt.Fprintf(&b, "\nRemembered from earlier chats with this player:\n%s\n", th.Memory)
	}
	blocks = append(blocks, anthropic.TextBlockParam{Text: b.String()})
	return blocks
}

func strProp(desc string) map[string]any {
	return map[string]any{"type": "string", "description": desc}
}
func intProp(desc string) map[string]any {
	return map[string]any{"type": "integer", "description": desc}
}
func objProp(desc string) map[string]any {
	return map[string]any{"type": "object", "description": desc, "additionalProperties": true}
}

func newTool(name, desc string, props map[string]any, required ...string) anthropic.ToolUnionParam {
	p := anthropic.ToolParam{Name: name, Description: param.NewOpt(desc),
		InputSchema: anthropic.ToolInputSchemaParam{Properties: props, Required: required}}
	return anthropic.ToolUnionParam{OfTool: &p}
}
