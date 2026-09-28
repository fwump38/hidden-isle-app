package db

import "time"

// AIUsage tracks one user's token spend for one calendar month (YYYY-MM), so the Seer's
// per-player and table-wide budget caps can be enforced without calling the Anthropic API.
// Its table name stays chat_usages, from when this was the in-app chat's usage table, so
// existing spend history carries over unchanged.
type AIUsage struct {
	ID           uint      `gorm:"primaryKey" json:"id"`
	UserID       uint      `gorm:"not null;uniqueIndex:idx_chat_usage_user_month" json:"user_id"`
	Month        string    `gorm:"not null;uniqueIndex:idx_chat_usage_user_month" json:"month"`
	InputTokens  int64     `json:"input_tokens"`
	OutputTokens int64     `json:"output_tokens"`
	CostUSD      float64   `json:"cost_usd"`
	UpdatedAt    time.Time `json:"updated_at"`
}

func (AIUsage) TableName() string { return "chat_usages" }
