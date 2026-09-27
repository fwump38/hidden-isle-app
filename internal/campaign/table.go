package campaign

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"strings"

	"github.com/fwump38/hidden-isle-app/internal/db"
)

// ---------------------------------------------------------------- the TV view

// TableKey returns the campaign's secret for the read-only TV link, creating it if needed.
// rotate makes a new one (old links stop working). Seer only.
func (s *Service) TableKey(a Actor, campaignID uint, rotate bool) (string, error) {
	if !a.IsSeer() {
		return "", ErrForbidden
	}
	var c db.Campaign
	if err := s.DB.First(&c, campaignID).Error; err != nil {
		return "", notFound(err)
	}
	if c.TableKey != "" && !rotate {
		return c.TableKey, nil
	}
	b := make([]byte, 18)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	key := base64.RawURLEncoding.EncodeToString(b)
	return key, s.DB.Model(&c).Update("table_key", key).Error
}

// CheckTableKey reports whether key opens the campaign's TV view.
func (s *Service) CheckTableKey(campaignID uint, key string) bool {
	var c db.Campaign
	if key == "" || s.DB.First(&c, campaignID).Error != nil || c.TableKey == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(c.TableKey), []byte(key)) == 1
}

// PublicAgent is what the table may see of an Agent.
type PublicAgent struct {
	ID       uint                `json:"id"`
	Name     string              `json:"name"`
	Class    string              `json:"class"`
	Status   string              `json:"status"`
	Harm     map[string][]string `json:"harm"`
	LoadUsed int                 `json:"load_used"`
}

// PublicState is everything players may see about the table right now, for the TV view.
type PublicState struct {
	Campaign    db.Campaign    `json:"campaign"`
	Agents      []PublicAgent  `json:"agents"`
	Clocks      []db.Clock     `json:"clocks"`
	Adversaries []db.Adversary `json:"adversaries"`
	Session     *db.Session    `json:"session,omitempty"`
	Handout     *db.Handout    `json:"handout,omitempty"`
}

// PublicState reads the campaign as a player would see it: no Seer-only clocks, hidden
// adversaries, secrets or session prep. Callers must have checked the table key.
func (s *Service) PublicState(campaignID uint) (*PublicState, error) {
	ps := &PublicState{}
	if err := s.DB.First(&ps.Campaign, campaignID).Error; err != nil {
		return nil, notFound(err)
	}
	var agents []db.Agent
	s.DB.Where("campaign_id = ? AND status IN ?", campaignID, []string{"Active", "Resting", "Captured"}).Order("name").Find(&agents)
	for _, a := range agents {
		ps.Agents = append(ps.Agents, PublicAgent{ID: a.ID, Name: a.Name, Class: a.Class, Status: a.Status, Harm: a.Harm, LoadUsed: a.LoadUsed})
	}
	s.DB.Where("campaign_id = ? AND visibility <> ? AND status = ?", campaignID, db.VisSeer, "Running").Order("name").Find(&ps.Clocks)
	s.DB.Where("campaign_id = ? AND hidden = ?", campaignID, false).Order("major desc, name").Find(&ps.Adversaries)
	for i := range ps.Adversaries {
		ps.Adversaries[i].Secrets = ""
	}
	var sess db.Session
	if s.DB.Where("campaign_id = ?", campaignID).Order("number desc").First(&sess).Error == nil {
		sess.Prep, sess.Divination, sess.NextTime = "", "", ""
		ps.Session = &sess
	}
	var h db.Handout
	if s.DB.Where("campaign_id = ? AND on_table = ? AND to_user_id IS NULL", campaignID, true).Order("id desc").First(&h).Error == nil {
		ps.Handout = &h
	}
	return ps, nil
}

// ---------------------------------------------------------------- handouts

// PushHandout stores a handout (Seer only). The caller broadcasts it.
func (s *Service) PushHandout(a Actor, h *db.Handout) error {
	if !a.IsSeer() {
		return ErrForbidden
	}
	if err := s.canView(s.DB, a, h.CampaignID); err != nil {
		return err
	}
	h.Title, h.Body = strings.TrimSpace(h.Title), strings.TrimSpace(h.Body)
	if h.Title == "" && h.Body == "" && h.Card == "" && h.ImageURL == "" {
		return errors.New("a handout needs a title, text, a card or an image")
	}
	if h.ImageURL != "" && !strings.HasPrefix(h.ImageURL, "https://") && !strings.HasPrefix(h.ImageURL, "http://") {
		return errors.New("the image must be a web link (https://…)")
	}
	if h.ToUserID != nil {
		var n int64
		s.DB.Model(&db.Member{}).Where("campaign_id = ? AND user_id = ?", h.CampaignID, *h.ToUserID).Count(&n)
		if n == 0 {
			return errors.New("that player isn't in this campaign")
		}
		h.OnTable = false // private handouts never go on the TV
	}
	return s.DB.Create(h).Error
}

// Handouts lists the handouts the actor may see, newest first.
func (s *Service) Handouts(a Actor, campaignID uint, limit int) ([]db.Handout, error) {
	if err := s.canView(s.DB, a, campaignID); err != nil {
		return nil, err
	}
	q := s.DB.Where("campaign_id = ?", campaignID).Order("id desc")
	if !a.IsSeer() {
		q = q.Where("to_user_id IS NULL OR to_user_id = ?", a.User.ID)
	}
	if limit <= 0 {
		limit = 20
	}
	var out []db.Handout
	return out, q.Limit(limit).Find(&out).Error
}
