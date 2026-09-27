package campaign

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"gorm.io/gorm"

	"github.com/fwump38/hidden-isle-app/internal/db"
)

// AssignAgent moves an Agent into a campaign, or out of any campaign (campaignID 0). The owner
// may move it into campaigns they belong to; the Seer may move any Agent, which adds its player
// to the campaign. Its contacts move with it.
func (s *Service) AssignAgent(a Actor, agentID, campaignID uint, o Opts) error {
	k, _ := kindOf("agent")
	return s.DB.Transaction(func(tx *gorm.DB) error {
		var ag db.Agent
		if err := tx.First(&ag, agentID).Error; err != nil {
			return notFound(err)
		}
		if err := s.agentAccess(tx, a, &ag); err != nil {
			return err
		}
		if !ownsAgent(a, &ag) {
			return ErrForbidden
		}
		if ag.CampaignID == campaignID {
			return nil
		}
		if campaignID != 0 {
			var c db.Campaign
			if err := tx.First(&c, campaignID).Error; err != nil {
				return notFound(err)
			}
			if err := s.canView(tx, a, campaignID); err != nil {
				return err
			}
			if a.IsSeer() && ag.OwnerID != nil {
				if err := tx.Where(db.Member{CampaignID: campaignID, UserID: *ag.OwnerID}).FirstOrCreate(&db.Member{}).Error; err != nil {
					return err
				}
			}
		}
		from, _ := json.Marshal(ag.CampaignID)
		to, _ := json.Marshal(campaignID)
		old := ag.CampaignID
		ag.CampaignID = campaignID
		if err := tx.Model(&ag).Update("campaign_id", campaignID).Error; err != nil {
			return err
		}
		if err := tx.Model(&db.Contact{}).Where("agent_id = ?", ag.ID).Update("campaign_id", campaignID).Error; err != nil {
			return err
		}
		if o.Reason == "" {
			if campaignID == 0 {
				o.Reason = "left the campaign"
			} else {
				o.Reason = "joined the campaign"
			}
		}
		changes := []db.Change{{Field: "campaign_id", From: from, To: to}}
		if _, err := s.record(tx, a, k, &ag, "update", changes, nil, o); err != nil {
			return err
		}
		if old != 0 && campaignID != 0 {
			// Also note the departure in the old campaign's log.
			ag.CampaignID = old
			_, err := s.record(tx, a, k, &ag, "update", changes, nil, o)
			return err
		}
		return nil
	})
}

// DeleteCampaign removes a campaign and everything in it. Players' Agents survive, moved out of
// the campaign; Agents the Seer ran (no player) are deleted. This can't be undone. Seer only;
// confirm must equal the campaign's name.
func (s *Service) DeleteCampaign(a Actor, id uint, confirm string) error {
	if !a.IsSeer() {
		return ErrForbidden
	}
	return s.DB.Transaction(func(tx *gorm.DB) error {
		var c db.Campaign
		if err := tx.First(&c, id).Error; err != nil {
			return notFound(err)
		}
		if strings.TrimSpace(confirm) != c.Name {
			return fmt.Errorf("type the campaign's name (%s) to confirm", c.Name)
		}
		var npcs []uint
		tx.Model(&db.Agent{}).Where("campaign_id = ? AND owner_id IS NULL", id).Pluck("id", &npcs)
		if len(npcs) > 0 {
			if err := tx.Where("agent_id IN ?", npcs).Delete(&db.Contact{}).Error; err != nil {
				return err
			}
			if err := tx.Where("id IN ?", npcs).Delete(&db.Agent{}).Error; err != nil {
				return err
			}
		}
		if err := tx.Model(&db.Agent{}).Where("campaign_id = ?", id).Update("campaign_id", 0).Error; err != nil {
			return err
		}
		if err := tx.Model(&db.Contact{}).Where("campaign_id = ?", id).Update("campaign_id", 0).Error; err != nil {
			return err
		}
		if err := tx.Exec("DELETE FROM entry_events WHERE entry_id IN (SELECT id FROM entries WHERE campaign_id = ?)", id).Error; err != nil {
			return err
		}
		// History lines of surviving Agents move with them; everything else in the campaign goes.
		if err := tx.Model(&db.Entry{}).Where("campaign_id = ? AND kind = ?", id, "history").Update("campaign_id", 0).Error; err != nil {
			return err
		}
		for _, m := range []any{&db.Member{}, &db.Clock{}, &db.Adversary{}, &db.Session{}, &db.Territory{},
			&db.HouseRuling{}, &db.SeerNote{}, &db.Entry{}, &db.Event{}} {
			if err := tx.Where("campaign_id = ?", id).Delete(m).Error; err != nil {
				return err
			}
		}
		return tx.Delete(&c).Error
	})
}

// DeleteUser removes a player. Their Agents in campaigns stay, played by the Seer; their Agents
// outside any campaign and their private journals are deleted. Seer only; not for Seers.
func (s *Service) DeleteUser(a Actor, userID uint) error {
	if !a.IsSeer() {
		return ErrForbidden
	}
	return s.DB.Transaction(func(tx *gorm.DB) error {
		var u db.User
		if err := tx.First(&u, userID).Error; err != nil {
			return notFound(err)
		}
		if u.IsSeer() {
			return errors.New("the Seer's account can't be deleted here")
		}
		var private []uint
		tx.Model(&db.Agent{}).Where("owner_id = ? AND campaign_id = 0", userID).Pluck("id", &private)
		if len(private) > 0 {
			if err := tx.Where("agent_id IN ?", private).Delete(&db.Contact{}).Error; err != nil {
				return err
			}
			if err := tx.Where("id IN ?", private).Delete(&db.Agent{}).Error; err != nil {
				return err
			}
		}
		if err := tx.Model(&db.Agent{}).Where("owner_id = ?", userID).Update("owner_id", nil).Error; err != nil {
			return err
		}
		if err := tx.Where("author_id = ? AND visibility = ?", userID, db.VisPrivate).Delete(&db.Entry{}).Error; err != nil {
			return err
		}
		for _, m := range []any{&db.Member{}, &db.APIToken{}} {
			if err := tx.Where("user_id = ?", userID).Delete(m).Error; err != nil {
				return err
			}
		}
		return tx.Delete(&u).Error
	})
}
