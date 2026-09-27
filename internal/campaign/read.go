package campaign

import (
	"errors"
	"fmt"

	"gorm.io/gorm"

	"github.com/fwump38/hidden-isle-app/internal/db"
)

// ---------------------------------------------------------------- campaigns and members

// CreateCampaign makes a campaign with the six printed territories (Ref p. 2). Seer only.
func (s *Service) CreateCampaign(a Actor, c *db.Campaign, o Opts) error {
	if !a.IsSeer() {
		return ErrForbidden
	}
	snap, err := s.snap()
	if err != nil {
		return err
	}
	if c.Mode == "" {
		c.Mode = "group"
	}
	if c.Season == 0 {
		c.Season = 1
	}
	if err := s.Create(a, "campaign", c, o); err != nil {
		return err
	}
	for _, name := range snap.Campaign.Territories {
		if err := s.Create(a, "territory", &db.Territory{CampaignID: c.ID, Name: name}, Opts{Reason: "printed territory (Ref p. 2)"}); err != nil {
			return err
		}
	}
	return nil
}

// Campaigns lists the campaigns the actor can see.
func (s *Service) Campaigns(a Actor) ([]db.Campaign, error) {
	var out []db.Campaign
	q := s.DB.Order("archived, name")
	if a.User == nil {
		return nil, ErrForbidden
	}
	if !a.IsSeer() {
		q = q.Where("id IN (?)", s.DB.Model(&db.Member{}).Select("campaign_id").Where("user_id = ?", a.User.ID))
	}
	return out, q.Find(&out).Error
}

func (s *Service) Campaign(a Actor, id uint) (*db.Campaign, error) {
	if err := s.canView(s.DB, a, id); err != nil {
		return nil, err
	}
	var c db.Campaign
	if err := s.DB.First(&c, id).Error; err != nil {
		return nil, notFound(err)
	}
	return &c, nil
}

// Members lists the players in a campaign.
func (s *Service) Members(a Actor, campaignID uint) ([]db.User, error) {
	if err := s.canView(s.DB, a, campaignID); err != nil {
		return nil, err
	}
	var out []db.User
	return out, s.DB.Where("id IN (?)", s.DB.Model(&db.Member{}).Select("user_id").Where("campaign_id = ?", campaignID)).
		Order("name").Find(&out).Error
}

// SetMember adds or removes a player from a campaign. Seer only.
func (s *Service) SetMember(a Actor, campaignID, userID uint, member bool) error {
	if !a.IsSeer() {
		return ErrForbidden
	}
	if member {
		return s.DB.Where(db.Member{CampaignID: campaignID, UserID: userID}).FirstOrCreate(&db.Member{}).Error
	}
	return s.DB.Where("campaign_id = ? AND user_id = ?", campaignID, userID).Delete(&db.Member{}).Error
}

// ---------------------------------------------------------------- agents

// NewAgent creates an Agent with the class's pre-filled skills. campaignID 0 creates it outside
// any campaign (private to its player). Players always own what they create and may only put it
// in campaigns they belong to. When the Seer creates an Agent for a player in a campaign, the
// player is added to the campaign.
func (s *Service) NewAgent(a Actor, campaignID uint, name, class string, ownerID *uint, o Opts) (*db.Agent, error) {
	snap, err := s.snap()
	if err != nil {
		return nil, err
	}
	if a.User == nil {
		return nil, ErrForbidden
	}
	if !a.IsSeer() {
		ownerID = &a.User.ID
	}
	if campaignID != 0 {
		if err := s.canView(s.DB, a, campaignID); err != nil {
			return nil, err
		}
		if a.IsSeer() && ownerID != nil {
			if err := s.SetMember(a, campaignID, *ownerID, true); err != nil {
				return nil, err
			}
		}
	}
	ag := &db.Agent{CampaignID: campaignID, OwnerID: ownerID, Name: name, Class: class, Status: "Active",
		Skills: map[string]int{}, Harm: map[string][]string{}}
	if c := snap.Class(class); c != nil {
		for sk, n := range c.PrefilledSkills {
			ag.Skills[sk] = n
		}
		ag.Class = c.ID
	}
	if o.Reason == "" {
		o.Reason = "new Agent"
	}
	return ag, s.Create(a, "agent", ag, o)
}

// MyAgents lists the actor's own Agents, in every campaign and none.
func (s *Service) MyAgents(a Actor) ([]db.Agent, error) {
	if a.User == nil {
		return nil, ErrForbidden
	}
	var out []db.Agent
	return out, s.DB.Where("owner_id = ?", a.User.ID).Order("status = 'Active' desc, name").Find(&out).Error
}

func (s *Service) Agents(a Actor, campaignID uint) ([]db.Agent, error) {
	if err := s.canView(s.DB, a, campaignID); err != nil {
		return nil, err
	}
	var out []db.Agent
	return out, s.DB.Where("campaign_id = ?", campaignID).Order("status = 'Active' desc, name").Find(&out).Error
}

func (s *Service) Agent(a Actor, id uint) (*db.Agent, error) {
	var ag db.Agent
	if err := s.DB.First(&ag, id).Error; err != nil {
		return nil, notFound(err)
	}
	if err := s.agentAccess(s.DB, a, &ag); err != nil {
		return nil, err
	}
	return &ag, nil
}

// CanEditAgent reports whether the actor may change this Agent's sheet.
func (s *Service) CanEditAgent(a Actor, ag *db.Agent) bool { return ownsAgent(a, ag) }

// AgentEvents is one Agent's change log, wherever it happened (in campaigns or outside any).
func (s *Service) AgentEvents(a Actor, agentID uint, limit int) ([]db.Event, error) {
	ag, err := s.Agent(a, agentID)
	if err != nil {
		return nil, err
	}
	q := s.DB.Where("entity_type = ? AND entity_id = ?", "agent", ag.ID).Order("id desc")
	if !a.IsSeer() {
		q = q.Where("visibility = ? OR (visibility = ? AND owner_id = ?)", db.VisParty, db.VisOwner, a.User.ID)
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	var out []db.Event
	return out, q.Limit(limit).Find(&out).Error
}

// AgentHistory lists the history entries written for one Agent.
func (s *Service) AgentHistory(a Actor, agentID uint) ([]db.Entry, error) {
	ag, err := s.Agent(a, agentID)
	if err != nil {
		return nil, err
	}
	q := s.DB.Where("kind = ? AND agent_id = ?", "history", ag.ID).Order("created_at desc")
	if !ownsAgent(a, ag) {
		q = q.Where("visibility = ? AND published = ?", db.VisParty, true)
	}
	var out []db.Entry
	return out, q.Find(&out).Error
}

func (s *Service) Contacts(a Actor, agentID uint) ([]db.Contact, error) {
	ag, err := s.Agent(a, agentID)
	if err != nil {
		return nil, err
	}
	var out []db.Contact
	return out, s.DB.Where("agent_id = ?", ag.ID).Order("affection desc, name").Find(&out).Error
}

// ---------------------------------------------------------------- other records

// List returns the records of one kind in a campaign that the actor may see, with Seer-only
// fields blanked for players. dst must be a pointer to a slice of that kind's model.
func (s *Service) List(a Actor, kindName string, campaignID uint, dst any, order string) error {
	k, err := kindOf(kindName)
	if err != nil {
		return err
	}
	if err := s.canView(s.DB, a, campaignID); err != nil {
		return err
	}
	q := s.DB.Where("campaign_id = ?", campaignID)
	if order != "" {
		q = q.Order(order)
	}
	if !a.IsSeer() {
		switch kindName {
		case "seer_note":
			return ErrForbidden
		case "clock":
			q = q.Where("visibility <> ?", db.VisSeer)
		case "adversary":
			q = q.Where("hidden = ?", false)
		}
	}
	if err := q.Find(dst).Error; err != nil {
		return err
	}
	if !a.IsSeer() && len(k.secret) > 0 {
		blankSecrets(k, dst)
	}
	return nil
}

// Get loads one record the actor may see.
func (s *Service) Get(a Actor, kindName string, id uint) (any, error) {
	k, err := kindOf(kindName)
	if err != nil {
		return nil, err
	}
	obj := k.new()
	if err := s.DB.First(obj, id).Error; err != nil {
		return nil, notFound(err)
	}
	if err := s.canView(s.DB, a, k.campaign(obj)); err != nil {
		return nil, err
	}
	if !k.canRead(a, obj) {
		return nil, ErrNotFound
	}
	return s.redact(k, a, obj), nil
}

func blankSecrets(k *kind, dst any) {
	switch v := dst.(type) {
	case *[]db.Session:
		for i := range *v {
			(*v)[i].Prep, (*v)[i].Divination, (*v)[i].NextTime = "", "", ""
		}
	case *[]db.Adversary:
		for i := range *v {
			(*v)[i].Secrets = ""
		}
	default:
		panic(fmt.Sprintf("blankSecrets: add %T", dst))
	}
}

// ---------------------------------------------------------------- change log and writing

type EventFilter struct {
	EntityType string
	EntityID   uint
	SessionID  uint
	Limit      int
	BeforeID   uint // for paging
}

// Events returns the change log the actor may see, newest first.
func (s *Service) Events(a Actor, campaignID uint, f EventFilter) ([]db.Event, error) {
	if err := s.canView(s.DB, a, campaignID); err != nil {
		return nil, err
	}
	q := s.DB.Where("campaign_id = ?", campaignID).Order("id desc")
	if !a.IsSeer() {
		q = q.Where("visibility = ? OR (visibility = ? AND owner_id = ?)", db.VisParty, db.VisOwner, a.User.ID)
	}
	if f.EntityType != "" {
		q = q.Where("entity_type = ?", f.EntityType)
	}
	if f.EntityID != 0 {
		q = q.Where("entity_id = ?", f.EntityID)
	}
	if f.SessionID != 0 {
		q = q.Where("session_id = ?", f.SessionID)
	}
	if f.BeforeID != 0 {
		q = q.Where("id < ?", f.BeforeID)
	}
	if f.Limit <= 0 || f.Limit > 200 {
		f.Limit = 50
	}
	var out []db.Event
	return out, q.Limit(f.Limit).Find(&out).Error
}

// Entry kinds and who may write them.
var entryKinds = map[string]bool{"session_log": true, "recap": true, "history": true, "journal": true, "note": true}

// WriteEntry creates or updates long-form writing. Players may write journals and notes, and
// history lines for their own Agents; session logs and recaps are the Seer's.
func (s *Service) WriteEntry(a Actor, e *db.Entry) error {
	if !entryKinds[e.Kind] {
		return fmt.Errorf("unknown entry kind %q", e.Kind)
	}
	if e.Kind == "history" {
		ag, err := s.Agent(a, derefU(e.AgentID))
		if err != nil || !ownsAgent(a, ag) {
			return ErrForbidden
		}
		e.CampaignID = ag.CampaignID
	} else if err := s.canView(s.DB, a, e.CampaignID); err != nil {
		return err
	}
	if !a.IsSeer() {
		if e.Kind == "session_log" || e.Kind == "recap" {
			return ErrForbidden
		}
		if e.Kind == "history" {
			ag, err := s.Agent(a, derefU(e.AgentID))
			if err != nil || !s.CanEditAgent(a, ag) {
				return ErrForbidden
			}
		}
		if e.Visibility == db.VisSeer {
			return ErrForbidden
		}
	}
	switch e.Visibility {
	case db.VisSeer, db.VisParty, db.VisOwner, db.VisPrivate:
	case "":
		e.Visibility = db.VisOwner
	default:
		return fmt.Errorf("unknown visibility %q", e.Visibility)
	}
	if e.ID == 0 {
		e.AuthorID = a.User.ID
		return s.DB.Create(e).Error
	}
	var old db.Entry
	if err := s.DB.First(&old, e.ID).Error; err != nil {
		return notFound(err)
	}
	if (old.CampaignID != e.CampaignID && old.Kind != "history") || (!a.IsSeer() && old.AuthorID != a.User.ID) {
		return ErrForbidden
	}
	e.AuthorID, e.CreatedAt = old.AuthorID, old.CreatedAt
	return s.DB.Save(e).Error
}

// Entries lists the writing the actor may read.
func (s *Service) Entries(a Actor, campaignID uint, kind string, agentID uint) ([]db.Entry, error) {
	if err := s.canView(s.DB, a, campaignID); err != nil {
		return nil, err
	}
	q := s.DB.Where("campaign_id = ?", campaignID).Order("created_at desc")
	if kind != "" {
		q = q.Where("kind = ?", kind)
	}
	if agentID != 0 {
		q = q.Where("agent_id = ?", agentID)
	}
	me := a.User.ID
	if a.IsSeer() {
		// Everything except players' private journals.
		q = q.Where("visibility <> ? OR author_id = ?", db.VisPrivate, me)
	} else {
		q = q.Where("author_id = ? OR (visibility = ? AND published = ?)", me, db.VisParty, true)
	}
	var out []db.Entry
	return out, q.Find(&out).Error
}

func (s *Service) DeleteEntry(a Actor, id uint) error {
	var e db.Entry
	if err := s.DB.First(&e, id).Error; err != nil {
		return notFound(err)
	}
	if !a.IsSeer() && e.AuthorID != a.User.ID {
		return ErrForbidden
	}
	if !a.IsSeer() && e.CampaignID != 0 {
		if err := s.canView(s.DB, a, e.CampaignID); err != nil {
			return err
		}
	}
	return s.DB.Delete(&e).Error
}

func derefU(p *uint) uint {
	if p == nil {
		return 0
	}
	return *p
}

// IsForbidden reports whether err means "not allowed" (for choosing an HTTP status).
func IsForbidden(err error) bool { return errors.Is(err, ErrForbidden) }

// IsNotFound reports whether err means "no such record".
func IsNotFound(err error) bool {
	return errors.Is(err, ErrNotFound) || errors.Is(err, gorm.ErrRecordNotFound)
}
