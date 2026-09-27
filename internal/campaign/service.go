// Package campaign holds every read and write of campaign state. Each write checks the actor's
// permissions, validates against the rules data, and records a raw event in the same
// transaction. The web UI, the MCP tools and the in-app chat all go through here, so the
// checks can't drift apart.
package campaign

import (
	"errors"
	"fmt"
	"strings"

	"gorm.io/gorm"

	"github.com/fwump38/hidden-isle-app/internal/db"
	"github.com/fwump38/hidden-isle-app/internal/gamedata"
)

var (
	ErrForbidden = errors.New("you don't have access to that")
	ErrNotFound  = errors.New("not found")
	ErrNoData    = errors.New("rules data hasn't loaded yet")
)

// ValidationError lists every rule a change would break.
type ValidationError struct{ Problems []string }

func (e *ValidationError) Error() string {
	return "that would break the rules: " + strings.Join(e.Problems, "; ")
}

// ConflictError means the record changed since the event being undone.
type ConflictError struct{ Fields []string }

func (e *ConflictError) Error() string {
	return "changed again since then (" + strings.Join(e.Fields, ", ") + "); undo the later change first"
}

// Actor is who is acting and through which door.
type Actor struct {
	User *db.User
	Via  string // web | mcp | chat | system
}

func (a Actor) IsSeer() bool { return a.User.IsSeer() }

func (a Actor) id() *uint {
	if a.User == nil {
		return nil
	}
	id := a.User.ID
	return &id
}

func (a Actor) name() string {
	n := "system"
	if a.User != nil {
		n = a.User.Name
	}
	if a.Via == "mcp" || a.Via == "chat" {
		n += " (Claude)"
	}
	return n
}

type Service struct {
	DB   *gorm.DB
	Data *gamedata.Store
	// OnEvent, if set, is told about every recorded change (for live updates). It must not block.
	OnEvent func(db.Event)
}

func New(g *gorm.DB, data *gamedata.Store) *Service { return &Service{DB: g, Data: data} }

func (s *Service) snap() (*gamedata.Snapshot, error) {
	if s.Data == nil || s.Data.Current() == nil {
		return nil, ErrNoData
	}
	return s.Data.Current(), nil
}

// canView reports whether the actor may see the campaign at all.
func (s *Service) canView(tx *gorm.DB, a Actor, campaignID uint) error {
	if a.User == nil {
		return ErrForbidden
	}
	if a.IsSeer() {
		return nil
	}
	var n int64
	tx.Model(&db.Member{}).Where("campaign_id = ? AND user_id = ?", campaignID, a.User.ID).Count(&n)
	if n == 0 {
		return ErrForbidden
	}
	return nil
}

func notFound(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrNotFound
	}
	return err
}

func problems(ps []string) error {
	if len(ps) == 0 {
		return nil
	}
	return &ValidationError{Problems: ps}
}

func inRange(ps *[]string, label string, v int, r gamedata.Range) {
	if v < r.Min || v > r.Max {
		*ps = append(*ps, fmt.Sprintf("%s must be %d-%d (%s)", label, r.Min, r.Max, r.Page))
	}
}

func oneOf(ps *[]string, label, v string, allowed []string) {
	if v == "" {
		return
	}
	for _, a := range allowed {
		if a == v {
			return
		}
	}
	*ps = append(*ps, fmt.Sprintf("%s %q isn't one of %s", label, v, strings.Join(allowed, ", ")))
}
