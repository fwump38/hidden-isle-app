package db

import (
	"encoding/json"
	"time"
)

// Visibility says who may read a record or log entry.
type Visibility string

const (
	VisSeer    Visibility = "seer"    // the Seer only
	VisParty   Visibility = "party"   // everyone in the campaign
	VisOwner   Visibility = "owner"   // the owning player and the Seer
	VisPrivate Visibility = "private" // the author only (a player's private journal)
)

// The json tags double as the field names used in the change log and in patches, and they
// match the GORM column names, so keep them snake_case and unique per model.

type Campaign struct {
	ID              uint      `gorm:"primaryKey" json:"id"`
	Name            string    `gorm:"not null" json:"name"`
	Mode            string    `gorm:"not null;default:group" json:"mode"` // group | solitaire | Seer-less
	Merciful        bool      `json:"merciful"`
	Season          int       `gorm:"not null;default:1" json:"season"`
	ScenariosPlayed int       `json:"scenarios_played"`
	HandMascot      string    `json:"hand_mascot"`
	HandName        string    `json:"hand_name"`
	Table           string    `json:"table"`   // session length, cadence, lines and veils, tone
	Options         string    `json:"options"` // optional rules in use
	OpenThreads     string    `json:"open_threads"`
	Archived        bool      `json:"archived"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// Member lets a player see a campaign. The Seer sees every campaign.
type Member struct {
	CampaignID uint      `gorm:"primaryKey" json:"campaign_id"`
	UserID     uint      `gorm:"primaryKey" json:"user_id"`
	User       User      `gorm:"constraint:OnDelete:CASCADE" json:"-"`
	CreatedAt  time.Time `json:"created_at"`
}

// AgentAbility is an ability on a sheet: a class ability from the rules data, or a custom one
// (a ritual, an ability taught by a contact from another class, a house rule).
type AgentAbility struct {
	ID     string `json:"id,omitempty"`     // rules-data ability id, e.g. "evil-eye"
	Name   string `json:"name,omitempty"`   // custom abilities only
	Text   string `json:"text,omitempty"`   // custom abilities only
	Source string `json:"source,omitempty"` // class | contact | ritual | other
	Notes  string `json:"notes,omitempty"`
}

type AgentProficiency struct {
	School   string `json:"school"`
	Rank     string `json:"rank"`     // Novice | Adept | Master
	Boxes    int    `json:"boxes"`    // rank boxes ticked: 0 Novice, 1 Adept, 2 first Master box, 3 Master
	Segments int    `json:"segments"` // proficiency clock (6)
}

type AgentItem struct {
	Name      string `json:"name"`
	SingleUse bool   `json:"single_use,omitempty"`
	Used      bool   `json:"used,omitempty"` // pulled this scenario
}

// Agent is a player character sheet. Players may see every Agent (p. 28).
type Agent struct {
	ID         uint  `gorm:"primaryKey" json:"id"`
	CampaignID uint  `gorm:"not null;index" json:"campaign_id"`
	OwnerID    *uint `gorm:"index" json:"owner_id"` // the player; nil = run by the Seer
	Version    int   `gorm:"not null;default:1" json:"-"`

	Name    string `gorm:"not null" json:"name"`
	Class   string `gorm:"not null" json:"class"` // rules-data class id
	Status  string `gorm:"not null;default:Active" json:"status"`
	Age     string `json:"age"`
	Culture string `json:"culture"`
	Look    string `json:"look"`
	Why     string `json:"why"` // why they came to Dioscoria

	ChildPhrase string `json:"child_phrase"`
	ChildCard   string `json:"child_card"`
	AdultVerb   string `json:"adult_verb"` // survived | flourished
	AdultPhrase string `json:"adult_phrase"`
	AdultCard   string `json:"adult_card"`

	Burden      string `json:"burden"`
	BurdenCard  string `json:"burden_card"`
	BurdenTrack int    `json:"burden_track"`
	Ideal       string `json:"ideal"`
	IdealCard   string `json:"ideal_card"`
	IdealTrack  int    `json:"ideal_track"`

	Vices            []string `gorm:"serializer:json" json:"vices"`
	Virtues          []string `gorm:"serializer:json" json:"virtues"`
	FulfilledVirtues []string `gorm:"serializer:json" json:"fulfilled_virtues"`

	Skills         map[string]int      `gorm:"serializer:json" json:"skills"`          // skill name → points
	UnlockedFourth []string            `gorm:"serializer:json" json:"unlocked_fourth"` // skills with the 4th pip unlocked
	Harm           map[string][]string `gorm:"serializer:json" json:"harm"`            // suit → marks (P, S, T), at most 2

	XPSwords    int `json:"xp_swords"`
	XPWands     int `json:"xp_wands"`
	XPCups      int `json:"xp_cups"`
	XPPentacles int `json:"xp_pentacles"`
	XPAbility   int `json:"xp_ability"`

	Abilities      []AgentAbility     `gorm:"serializer:json" json:"abilities"`
	Proficiencies  []AgentProficiency `gorm:"serializer:json" json:"proficiencies"`
	MagicalSources []string           `gorm:"serializer:json" json:"magical_sources"`
	Items          []AgentItem        `gorm:"serializer:json" json:"items"`
	LoadUsed       int                `json:"load_used"`

	Notes     string    `json:"notes"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type Contact struct {
	ID          uint      `gorm:"primaryKey" json:"id"`
	CampaignID  uint      `gorm:"not null;index" json:"campaign_id"`
	AgentID     uint      `gorm:"not null;index" json:"agent_id"`
	Name        string    `gorm:"not null" json:"name"`
	Kind        string    `json:"kind"`
	Card        string    `json:"card"`
	Land        string    `json:"land"`
	Description string    `json:"description"`
	Affection   int       `json:"affection"`
	Distance    int       `json:"distance"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type Session struct {
	ID          uint       `gorm:"primaryKey" json:"id"`
	CampaignID  uint       `gorm:"not null;index" json:"campaign_id"`
	Number      int        `json:"number"`
	Title       string     `gorm:"not null" json:"title"`
	Date        *time.Time `json:"date"`
	Status      string     `gorm:"not null;default:Prep" json:"status"`
	Adventure   string     `json:"adventure"` // rules-data adventure id, or "" for an original scenario
	MissionType string     `json:"mission_type"`
	Territory   string     `json:"territory"`
	Summary     string     `json:"summary"` // the divination summary as read to the players
	// Seer-only.
	Prep       string    `json:"prep"`
	Divination string    `json:"divination"`
	NextTime   string    `json:"next_time"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// Adversary is a row on the Adversary Sheet, which players may read (p. 28). Hidden ones are
// the Seer's prep; Secrets never leave the Seer.
type Adversary struct {
	ID          uint      `gorm:"primaryKey" json:"id"`
	CampaignID  uint      `gorm:"not null;index" json:"campaign_id"`
	Name        string    `gorm:"not null" json:"name"`
	Leader      string    `json:"leader"`
	Plot        string    `json:"plot"`
	Motivation  string    `json:"motivation"`
	Members     string    `json:"members"`
	Symbol      string    `json:"symbol"`
	Progress    int       `json:"progress"`
	TrackLength int       `json:"track_length"`
	Status      string    `gorm:"not null;default:Rumored" json:"status"`
	Major       bool      `json:"major"`
	Hidden      bool      `json:"hidden"`  // not on the sheet yet
	Secrets     string    `json:"secrets"` // Seer-only
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type Territory struct {
	ID         uint      `gorm:"primaryKey" json:"id"`
	CampaignID uint      `gorm:"not null;index" json:"campaign_id"`
	Name       string    `gorm:"not null" json:"name"`
	Events     string    `json:"events"`
	Contacts   string    `json:"contacts"`
	Notes      string    `json:"notes"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

type Clock struct {
	ID         uint       `gorm:"primaryKey" json:"id"`
	CampaignID uint       `gorm:"not null;index" json:"campaign_id"`
	Name       string     `gorm:"not null" json:"name"` // what happens when it fills (p. 86)
	Segments   int        `gorm:"not null" json:"segments"`
	Filled     int        `json:"filled"`
	Scope      string     `json:"scope"`
	LinkedTo   string     `json:"linked_to"`
	AgentID    *uint      `gorm:"index" json:"agent_id"` // character/ability/proficiency clocks
	Status     string     `gorm:"not null;default:Running" json:"status"`
	Visibility Visibility `gorm:"not null;default:party" json:"visibility"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
}

type HouseRuling struct {
	ID         uint      `gorm:"primaryKey" json:"id"`
	CampaignID uint      `gorm:"not null;index" json:"campaign_id"`
	Ruling     string    `gorm:"not null" json:"ruling"`
	Page       string    `json:"page"` // the page it interprets
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// SeerNote is a Seer-only page: adversary secrets, twists, what the Hand hasn't learned.
type SeerNote struct {
	ID         uint      `gorm:"primaryKey" json:"id"`
	CampaignID uint      `gorm:"not null;index" json:"campaign_id"`
	Title      string    `gorm:"not null" json:"title"`
	Body       string    `json:"body"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// Event is one raw change: who changed what, from what, to what, and why. Written in the same
// transaction as the change itself.
type Event struct {
	ID         uint            `gorm:"primaryKey" json:"id"`
	CampaignID uint            `gorm:"not null;index" json:"campaign_id"`
	SessionID  *uint           `gorm:"index" json:"session_id,omitempty"`
	ActorID    *uint           `json:"actor_id,omitempty"`
	ActorName  string          `json:"actor_name"`
	Via        string          `json:"via"`         // web | mcp | chat | system
	EntityType string          `json:"entity_type"` // agent, contact, clock, …
	EntityID   uint            `json:"entity_id"`
	EntityName string          `json:"entity_name"`
	Action     string          `json:"action"`  // create | update | delete | revert
	Summary    string          `json:"summary"` // e.g. "Ines: harm.Cups [P P] → [P]"
	Changes    []Change        `gorm:"serializer:json" json:"changes"`
	Snapshot   json.RawMessage `gorm:"type:text" json:"-"` // full record for create/delete (undo)
	Reason     string          `json:"reason"`
	Override   bool            `json:"override,omitempty"` // Seer broke a limit on purpose
	Visibility Visibility      `gorm:"not null;index" json:"visibility"`
	OwnerID    *uint           `json:"owner_id,omitempty"` // for owner visibility
	RevertOf   *uint           `json:"revert_of,omitempty"`
	RevertedBy *uint           `json:"reverted_by,omitempty"`
	CreatedAt  time.Time       `gorm:"index" json:"created_at"`
}

// Change is one field's before and after, as JSON.
type Change struct {
	Field string          `json:"field"`
	From  json.RawMessage `json:"from"`
	To    json.RawMessage `json:"to"`
}

// Entry is long-form writing by a person: a session log, a recap, an Agent history line, a
// journal page. Claude never invents these; at most it tidies what someone wrote.
type Entry struct {
	ID         uint       `gorm:"primaryKey" json:"id"`
	CampaignID uint       `gorm:"not null;index" json:"campaign_id"`
	SessionID  *uint      `gorm:"index" json:"session_id,omitempty"`
	AgentID    *uint      `gorm:"index" json:"agent_id,omitempty"`
	AuthorID   uint       `gorm:"not null" json:"author_id"`
	Kind       string     `gorm:"not null" json:"kind"` // session_log | recap | history | journal | note
	Title      string     `json:"title"`
	Body       string     `json:"body"`
	Visibility Visibility `gorm:"not null;default:owner" json:"visibility"`
	Published  bool       `json:"published"`
	Events     []Event    `gorm:"many2many:entry_events" json:"-"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
}
