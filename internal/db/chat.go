package db

import "time"

// ChatThread is one player's ongoing conversation with the in-app assistant for one campaign
// (or campaign 0, for chat before a player has joined one). There's one per player per campaign.
type ChatThread struct {
	ID         uint   `gorm:"primaryKey" json:"id"`
	UserID     uint   `gorm:"not null;uniqueIndex:idx_chat_thread_user_campaign" json:"user_id"`
	CampaignID uint   `gorm:"uniqueIndex:idx_chat_thread_user_campaign" json:"campaign_id"`
	Title      string `json:"title"`
	// Memory is a short rolling note the assistant keeps between chats (chosen names, open
	// questions, decisions made) so it doesn't have to be re-explained. The player and the Seer
	// can both read and edit it; it's never hidden from either of them.
	Memory    string    `json:"memory"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ChatMessage is one turn of a thread. Only the final text of each turn is kept; the tool calls
// that produced an assistant turn are summarized in Tools for the Seer's transcript view, not
// replayed on the next turn.
type ChatMessage struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	ThreadID  uint      `gorm:"not null;index" json:"thread_id"`
	Role      string    `gorm:"not null" json:"role"` // user | assistant
	Text      string    `json:"text"`
	Tools     string    `json:"tools,omitempty"` // brief note of tools used, e.g. "search_rules, get_class"
	CreatedAt time.Time `json:"created_at"`
}

// ChatSuggestion is a change the assistant proposed to one of the player's own Agents during
// character-creation help. Nothing is applied until the player approves it here.
type ChatSuggestion struct {
	ID        uint       `gorm:"primaryKey" json:"id"`
	ThreadID  uint       `gorm:"not null;index" json:"thread_id"`
	AgentID   uint       `gorm:"not null;index" json:"agent_id"`
	Fields    string     `json:"fields"` // JSON object of field → value, same shape as a campaign.Patch
	Summary   string     `json:"summary"`
	Why       string     `json:"why"`
	Status    string     `gorm:"not null;default:pending" json:"status"` // pending | applied | dismissed
	CreatedAt time.Time  `json:"created_at"`
	DecidedAt *time.Time `json:"decided_at"`
}

// ChatUsage tracks one user's token spend for one calendar month (YYYY-MM), so the Seer's
// per-player and table-wide budget caps can be enforced without calling the Anthropic API.
type ChatUsage struct {
	ID           uint      `gorm:"primaryKey" json:"id"`
	UserID       uint      `gorm:"not null;uniqueIndex:idx_chat_usage_user_month" json:"user_id"`
	Month        string    `gorm:"not null;uniqueIndex:idx_chat_usage_user_month" json:"month"`
	InputTokens  int64     `json:"input_tokens"`
	OutputTokens int64     `json:"output_tokens"`
	CostUSD      float64   `json:"cost_usd"`
	UpdatedAt    time.Time `json:"updated_at"`
}
