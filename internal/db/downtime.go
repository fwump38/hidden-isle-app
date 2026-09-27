package db

import "time"

// DowntimeVice is one vice's harm, with the suit the player chose for it (p. 23: the player
// picks the suit when none is specified).
type DowntimeVice struct {
	Vice string `json:"vice"`
	Suit string `json:"suit"`
}

// DowntimeAction is one of the player's chosen downtime actions (p. 68). Only the fields the
// chosen Kind needs are filled in; the rest are zero.
type DowntimeAction struct {
	Kind string `json:"kind"` // heal | train | prepare | reflect | new_contact | visit_contact

	// heal
	HarmType string `json:"harm_type,omitempty"` // P or S
	Amount   int    `json:"amount,omitempty"`
	Suit     string `json:"suit,omitempty"`

	// train: track is a suit name or "ability", or "proficiency:<school>" for a proficiency clock
	Track string `json:"track,omitempty"`

	// prepare: narrative; ClockID+Segments optionally ticks an existing ritual clock
	ClockID  uint `json:"clock_id,omitempty"`
	Segments int  `json:"segments,omitempty"`

	// reflect
	Trait      string `json:"trait,omitempty"` // burden | ideal
	TrackDelta int    `json:"track_delta,omitempty"`

	// new_contact
	District           string `json:"district,omitempty"`
	ContactName        string `json:"contact_name,omitempty"`
	ContactCard        string `json:"contact_card,omitempty"`
	ContactDescription string `json:"contact_description,omitempty"`

	// visit_contact: Activity is heal | train | reflect | heart_to_heart
	ContactID uint   `json:"contact_id,omitempty"`
	Activity  string `json:"activity,omitempty"`

	Note string `json:"note,omitempty"` // the player's vignette/description for this action
}

// DowntimeSubmission is a player's downtime plan, held for the Seer's approval before it
// touches the Agent's sheet. Vice harm and contact drift aren't stored here: they're computed
// fresh from the Agent's current state when the Seer approves.
type DowntimeSubmission struct {
	ID              uint             `gorm:"primaryKey" json:"id"`
	CampaignID      uint             `gorm:"not null;index" json:"campaign_id"`
	AgentID         uint             `gorm:"not null;index" json:"agent_id"`
	Status          string           `gorm:"not null;default:pending" json:"status"` // pending | approved | rejected
	Vices           []DowntimeVice   `gorm:"serializer:json" json:"vices"`
	Actions         []DowntimeAction `gorm:"serializer:json" json:"actions"`
	ExtraAction     bool             `json:"extra_action"`      // a 3rd (or 4th, solo) action, costing 2 spiritual harm
	ExtraActionSuit string           `json:"extra_action_suit"` // which suit takes that harm
	PlayerNote      string           `json:"player_note"`
	SeerNote        string           `json:"seer_note"`
	CreatedAt       time.Time        `json:"created_at"`
	DecidedAt       *time.Time       `json:"decided_at,omitempty"`
}
