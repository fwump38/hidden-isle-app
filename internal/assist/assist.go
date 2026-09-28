// Package assist is the campaign's in-place AI help: the agent wizard's "describe what you have
// in mind" suggestions, the writing assistant on free-form text fields, and the Seer's structured
// suggestion boxes, all on the Seer's own Anthropic API budget. It never touches claude.ai or the
// Seer's MCP session; each request is built fresh from the database and nothing is saved until
// the caller applies it themselves, which goes through campaign.Service.Update exactly like every
// other edit.
package assist

import (
	"errors"
	"log/slog"
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
	ErrBudget = errors.New("the monthly AI budget has been reached; ask the Seer to raise it")
	ErrRate   = errors.New("slow down a little before trying again")
)

const (
	rateLimitWindow = 5 * time.Minute
	// The creation wizard asks for suggestions more often than anything else, so it has its own,
	// looser limit (the monthly budgets still apply to everything).
	suggestRateCount = 30
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

	mu            sync.Mutex
	recentSuggest map[uint][]time.Time // wizard/suggest-box requests
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
		client: anthropic.NewClient(opts...), recentSuggest: map[uint][]time.Time{}}
}

func (s *Service) checkRateIn(m map[uint][]time.Time, userID uint, limit int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	var recent []time.Time
	for _, t := range m[userID] {
		if now.Sub(t) < rateLimitWindow {
			recent = append(recent, t)
		}
	}
	if len(recent) >= limit {
		return ErrRate
	}
	m[userID] = append(recent, now)
	return nil
}

func currentMonth() string { return time.Now().Format("2006-01") }

func (s *Service) monthSpend(month string, userID uint) (userUSD, globalUSD float64) {
	var row struct{ Total float64 }
	s.db.Model(&db.AIUsage{}).Where("month = ? AND user_id = ?", month, userID).
		Select("COALESCE(SUM(cost_usd), 0) AS total").Scan(&row)
	userUSD = row.Total
	s.db.Model(&db.AIUsage{}).Where("month = ?", month).
		Select("COALESCE(SUM(cost_usd), 0) AS total").Scan(&row)
	globalUSD = row.Total
	return
}

func (s *Service) cost(inTok, outTok int64) float64 {
	return float64(inTok)/1e6*s.cfg.PriceInPerMTok + float64(outTok)/1e6*s.cfg.PriceOutPerMTok
}

func (s *Service) recordUsage(month string, userID uint, inTok, outTok int64) {
	usd := s.cost(inTok, outTok)
	var u db.AIUsage
	if err := s.db.Where(db.AIUsage{UserID: userID, Month: month}).FirstOrCreate(&u).Error; err != nil {
		slog.Error("ai usage", "err", err)
		return
	}
	err := s.db.Model(&u).Updates(map[string]any{
		"input_tokens":  gorm.Expr("input_tokens + ?", inTok),
		"output_tokens": gorm.Expr("output_tokens + ?", outTok),
		"cost_usd":      gorm.Expr("cost_usd + ?", usd),
	}).Error
	if err != nil {
		slog.Error("ai usage", "err", err)
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

func strProp(desc string) map[string]any {
	return map[string]any{"type": "string", "description": desc}
}

func newTool(name, desc string, props map[string]any, required ...string) anthropic.ToolUnionParam {
	p := anthropic.ToolParam{Name: name, Description: param.NewOpt(desc),
		InputSchema: anthropic.ToolInputSchemaParam{Properties: props, Required: required}}
	return anthropic.ToolUnionParam{OfTool: &p}
}
