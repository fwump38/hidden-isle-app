package db

import (
	"time"

	"gorm.io/gorm"
)

// models lists every table AutoMigrate manages. Add new models here.
var models = []any{&SchemaMigration{}, &User{}, &APIToken{},
	&Campaign{}, &Member{}, &Agent{}, &Contact{}, &Session{}, &Adversary{}, &Territory{}, &Clock{},
	&HouseRuling{}, &SeerNote{}, &Event{}, &Entry{}}

// migrations are one-off steps AutoMigrate can't express (renames, data fixes). Append only.
var migrations = []struct {
	ID string
	Up func(tx *gorm.DB) error
}{}

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
