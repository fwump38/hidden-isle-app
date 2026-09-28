package db

import (
	"time"

	"gorm.io/gorm"
)

// models lists every table AutoMigrate manages. Add new models here.
var models = []any{&SchemaMigration{}, &User{}, &APIToken{},
	&Campaign{}, &Member{}, &Agent{}, &Contact{}, &Session{}, &Adversary{}, &Territory{}, &Clock{},
	&HouseRuling{}, &SeerNote{}, &Event{}, &Entry{}, &Handout{}, &DowntimeSubmission{},
	&AIUsage{}}

// migrations are one-off steps AutoMigrate can't express (renames, data fixes). Append only.
var migrations = []struct {
	ID string
	Up func(tx *gorm.DB) error
}{
	{ID: "2025_drop_chat_tables", Up: func(tx *gorm.DB) error {
		// The in-app chat is gone; chat_usages (now AIUsage) is the only table that survives it.
		return tx.Migrator().DropTable("chat_threads", "chat_messages", "chat_suggestions")
	}},
	{ID: "2025_merge_note_entries", Up: func(tx *gorm.DB) error {
		// The "note" entry kind is gone: a Seer-only one was really a Seer note, and every other
		// one is just a journal entry under a different name. Publish now only matters for
		// Everyone-visible entries, so everything else is marked published to match (it already
		// behaved that way: Entries() never checked Published for anything but party visibility).
		var notes []Entry
		if err := tx.Where("kind = ? AND visibility = ?", "note", VisSeer).Find(&notes).Error; err != nil {
			return err
		}
		for _, e := range notes {
			title := e.Title
			if title == "" {
				title = "Note"
			}
			sn := SeerNote{CampaignID: e.CampaignID, Title: title, Body: e.Body, CreatedAt: e.CreatedAt, UpdatedAt: e.UpdatedAt}
			if err := tx.Create(&sn).Error; err != nil {
				return err
			}
			if err := tx.Delete(&Entry{}, e.ID).Error; err != nil {
				return err
			}
		}
		if err := tx.Model(&Entry{}).Where("kind = ?", "note").Update("kind", "journal").Error; err != nil {
			return err
		}
		return tx.Model(&Entry{}).Where("visibility <> ?", VisParty).Update("published", true).Error
	}},
}

type SchemaMigration struct {
	ID        string `gorm:"primaryKey"`
	AppliedAt time.Time
}

type Role string

const (
	RoleSeer   Role = "seer"
	RolePlayer Role = "player"
)

// User is a person at the table. Players log in on the LAN with a PIN, or anywhere with SSO
// (their email). The Seer logs in with SSO, or on the LAN with a password.
type User struct {
	ID           uint    `gorm:"primaryKey"`
	Name         string  `gorm:"not null;uniqueIndex"`
	Email        *string `gorm:"uniqueIndex"` // lowercase; nil = no SSO mapping
	Role         Role    `gorm:"not null;default:player"`
	PINHash      string
	PasswordHash string
	Active       bool `gorm:"not null;default:true"`
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

func (u *User) IsSeer() bool { return u != nil && u.Role == RoleSeer }

// APIToken is a long-lived bearer token for MCP clients that can't do OAuth (e.g. Claude Code).
// Only the SHA-256 of the token is stored.
type APIToken struct {
	ID         uint   `gorm:"primaryKey"`
	UserID     uint   `gorm:"not null;index"`
	User       User   `gorm:"constraint:OnDelete:CASCADE"`
	Name       string `gorm:"not null"`
	Hash       string `gorm:"not null;uniqueIndex"`
	Prefix     string `gorm:"not null"` // first characters, shown in the admin list
	LastUsedAt *time.Time
	RevokedAt  *time.Time
	CreatedAt  time.Time
}
