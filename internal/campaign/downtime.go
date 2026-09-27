package campaign

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/fwump38/hidden-isle-app/internal/db"
	"github.com/fwump38/hidden-isle-app/internal/gamedata"
)

// Downtime (pp. 65-71): a player plans it on their own time, then the Seer approves it, at
// which point vice harm, contact drift and the chosen actions all apply to the sheet at once.
// Nothing changes on the Agent until approval. Character transformation (pp. 66-67, when a
// burden or ideal track is full) isn't part of this queue: it's a short guided conversation
// with several player choices (a new trait drawn from cards, whether to keep a linked vice or
// virtue, xp or a 4th pip), so the Seer and player make those changes on the sheet directly;
// DowntimeStatus flags when it's due.

const (
	DowntimeStatusPending  = "pending"
	DowntimeStatusApproved = "approved"
	DowntimeStatusRejected = "rejected"
)

var validActionKinds = map[string]bool{"heal": true, "train": true, "prepare": true, "reflect": true, "new_contact": true, "visit_contact": true}
var visitActivities = map[string]bool{"heal": true, "train": true, "reflect": true, "heart_to_heart": true}

// downtimeFreeActions is how many actions a submission may take before the 3rd (or, solo,
// 4th) costs 2 spiritual harm (p. 68, p. 98).
func downtimeFreeActions(l *gamedata.Limits, solo bool) int {
	if solo && l.Downtime.SolitaireFreeActions > 0 {
		return l.Downtime.SolitaireFreeActions
	}
	return l.Downtime.FreeActions
}

// SubmitDowntime validates a player's downtime plan and holds it for the Seer's approval.
// Nothing is applied to the Agent yet.
func (s *Service) SubmitDowntime(a Actor, agentID uint, sub *db.DowntimeSubmission) (*db.DowntimeSubmission, error) {
	snap, err := s.snap()
	if err != nil {
		return nil, err
	}
	ag, err := s.Agent(a, agentID)
	if err != nil {
		return nil, err
	}
	if !ownsAgent(a, ag) {
		return nil, ErrForbidden
	}
	if ag.CampaignID == 0 {
		return nil, errors.New("bring this Agent into a campaign before submitting downtime")
	}
	solo := false
	if c, err := s.Campaign(a, ag.CampaignID); err == nil && c.Mode == "solitaire" {
		solo = true
	}
	contacts, err := s.Contacts(a, agentID)
	if err != nil {
		return nil, err
	}
	if err := validateDowntimeVices(sub.Vices, ag.Vices); err != nil {
		return nil, err
	}
	free := downtimeFreeActions(&snap.Limits, solo)
	if len(sub.Actions) > free {
		if !sub.ExtraAction {
			return nil, fmt.Errorf("a %s action costs 2 spiritual harm (p. 68); check it and choose a suit for that harm", ordinal(free+1))
		}
		if _, err := suitName(sub.ExtraActionSuit); err != nil {
			return nil, fmt.Errorf("extra action: %w", err)
		}
	} else if len(sub.Actions) == 0 {
		return nil, errors.New("choose at least one downtime action, or leave downtime to just vice harm and contact drift")
	}
	if len(sub.Actions) > free+1 {
		return nil, fmt.Errorf("at most %d actions (%d free, 1 more for 2 spiritual harm)", free+1, free)
	}
	visits := 0
	for i, act := range sub.Actions {
		if err := validateDowntimeAction(act, ag, contacts, snap); err != nil {
			return nil, fmt.Errorf("action %d (%s): %w", i+1, act.Kind, err)
		}
		if act.Kind == "visit_contact" {
			visits++
		}
	}
	if visits > snap.Limits.Downtime.VisitsPerDowntime {
		return nil, fmt.Errorf("at most %d visit per downtime (p. 70)", snap.Limits.Downtime.VisitsPerDowntime)
	}
	sub.AgentID, sub.CampaignID, sub.Status = ag.ID, ag.CampaignID, DowntimeStatusPending
	sub.SeerNote, sub.DecidedAt = "", nil
	if err := s.DB.Create(sub).Error; err != nil {
		return nil, err
	}
	return sub, nil
}

func validateDowntimeVices(given []db.DowntimeVice, vices []string) error {
	if len(given) != len(vices) {
		return fmt.Errorf("each vice causes 1 spiritual harm (p. 66): choose a suit for all %d", len(vices))
	}
	seen := map[string]bool{}
	for _, v := range given {
		if !slices.Contains(vices, v.Vice) || seen[v.Vice] {
			return fmt.Errorf("%q isn't one of this Agent's current vices", v.Vice)
		}
		seen[v.Vice] = true
		if _, err := suitName(v.Suit); err != nil {
			return fmt.Errorf("vice %q: %w", v.Vice, err)
		}
	}
	return nil
}

func validateDowntimeAction(act db.DowntimeAction, ag *db.Agent, contacts []db.Contact, snap *gamedata.Snapshot) error {
	if !validActionKinds[act.Kind] {
		return fmt.Errorf("unknown kind %q", act.Kind)
	}
	l := snap.Limits.Downtime
	switch act.Kind {
	case "heal":
		if act.HarmType != "P" && act.HarmType != "S" {
			return errors.New("heal is physical (P) or spiritual (S); trauma needs a heart-to-heart or a virtue")
		}
		if act.Amount < 1 || act.Amount > l.HealMax {
			return fmt.Errorf("heal 1-%d harm (p. 68)", l.HealMax)
		}
	case "train":
		if _, err := downtimeTrainTarget(act.Track, ag); err != nil {
			return err
		}
	case "reflect":
		if act.Trait != "burden" && act.Trait != "ideal" {
			return errors.New("trait must be burden or ideal")
		}
		if act.TrackDelta != 1 && act.TrackDelta != -1 {
			return errors.New("reflect moves the track by ±1 (p. 68)")
		}
	case "new_contact":
		if strings.TrimSpace(act.ContactName) == "" {
			return errors.New("a new contact needs a name")
		}
	case "visit_contact":
		if !visitActivities[act.Activity] {
			return errors.New("activity must be heal, train, reflect or heart_to_heart")
		}
		var c *db.Contact
		for i := range contacts {
			if contacts[i].ID == act.ContactID {
				c = &contacts[i]
			}
		}
		if c == nil {
			return errors.New("that contact isn't on this Agent's sheet")
		}
		if act.Activity == "heart_to_heart" && c.Affection < 6 {
			return errors.New("a heart-to-heart needs affection 6 (p. 71)")
		}
		if act.Activity == "train" {
			if _, err := downtimeTrainTarget(act.Track, ag); err != nil {
				return err
			}
		}
		if act.Activity == "reflect" && act.Trait != "burden" && act.Trait != "ideal" {
			return errors.New("trait must be burden or ideal")
		}
	case "prepare":
		// Narrative by default; a Seer-chosen clock tick can be added at approval time too.
	}
	return nil
}

// downtimeTrainTarget checks a Train action's target: a skill suit, "ability", or
// "proficiency:<School>" naming a proficiency already on the sheet.
func downtimeTrainTarget(track string, ag *db.Agent) (string, error) {
	track = strings.TrimSpace(track)
	switch strings.ToLower(track) {
	case "swords", "wands", "cups", "pentacles", "ability":
		return track, nil
	}
	if school, ok := strings.CutPrefix(track, "proficiency:"); ok {
		for _, p := range ag.Proficiencies {
			if strings.EqualFold(p.School, school) {
				return school, nil
			}
		}
		return "", fmt.Errorf("no proficiency %q on the sheet yet; add it first", school)
	}
	return "", errors.New("train a suit, ability, or proficiency:<school>")
}

func ordinal(n int) string {
	switch n {
	case 1:
		return "1st"
	case 2:
		return "2nd"
	case 3:
		return "3rd"
	default:
		return fmt.Sprintf("%dth", n)
	}
}

// DowntimeSubmissions lists submissions the actor may see: the Seer sees every one in the
// campaign; a player sees only their own Agents' submissions.
func (s *Service) DowntimeSubmissions(a Actor, campaignID uint, status string) ([]db.DowntimeSubmission, error) {
	if err := s.canView(s.DB, a, campaignID); err != nil {
		return nil, err
	}
	q := s.DB.Where("campaign_id = ?", campaignID).Order("id desc")
	if status != "" {
		q = q.Where("status = ?", status)
	}
	if !a.IsSeer() {
		var ids []uint
		s.DB.Model(&db.Agent{}).Where("campaign_id = ? AND owner_id = ?", campaignID, a.User.ID).Pluck("id", &ids)
		q = q.Where("agent_id IN ?", ids)
	}
	var out []db.DowntimeSubmission
	return out, q.Find(&out).Error
}

func (s *Service) getSubmission(a Actor, id uint) (*db.DowntimeSubmission, error) {
	var sub db.DowntimeSubmission
	if err := s.DB.First(&sub, id).Error; err != nil {
		return nil, notFound(err)
	}
	if err := s.canView(s.DB, a, sub.CampaignID); err != nil {
		return nil, err
	}
	if !a.IsSeer() {
		ag, err := s.Agent(a, sub.AgentID)
		if err != nil || !ownsAgent(a, ag) {
			return nil, ErrForbidden
		}
	}
	return &sub, nil
}

// RejectDowntime sends a submission back to the player with a reason. Nothing on the sheet
// changes. Seer only.
func (s *Service) RejectDowntime(a Actor, id uint, reason string) error {
	if !a.IsSeer() {
		return ErrForbidden
	}
	sub, err := s.getSubmission(a, id)
	if err != nil {
		return err
	}
	if sub.Status != DowntimeStatusPending {
		return fmt.Errorf("already %s", sub.Status)
	}
	if strings.TrimSpace(reason) == "" {
		return errors.New("say why, so the player knows what to change")
	}
	return s.DB.Model(sub).Updates(map[string]any{"status": DowntimeStatusRejected, "seer_note": reason}).Error
}

// ApproveDowntime applies a pending submission's vice harm, contact drift and chosen actions to
// the Agent, in order, then marks it approved. Each step is its own logged change (so any one
// of them can be undone from the log). If a step fails partway (most likely: a harm suit is
// now full), the submission is left pending with the error, so the Seer can sort it out — with
// the sheet or the player — and approve again; nothing already applied is rolled back.
func (s *Service) ApproveDowntime(a Actor, id uint, note string) error {
	if !a.IsSeer() {
		return ErrForbidden
	}
	sub, err := s.getSubmission(a, id)
	if err != nil {
		return err
	}
	if sub.Status != DowntimeStatusPending {
		return fmt.Errorf("already %s", sub.Status)
	}
	snap, err := s.snap()
	if err != nil {
		return err
	}
	reason := "downtime: approved"
	if note != "" {
		reason = "downtime: " + note
	}
	o := Opts{Reason: reason}

	for _, v := range sub.Vices {
		if _, err := s.AddHarm(a, sub.AgentID, v.Suit, "S", snap.Limits.Downtime.ViceSpiritualHarm, Opts{Reason: fmt.Sprintf("downtime: vice (%s)", v.Vice)}); err != nil {
			return fmt.Errorf("vice %q: %w", v.Vice, err)
		}
	}
	if _, err := s.DriftContacts(a, sub.AgentID, o); err != nil {
		return fmt.Errorf("contact drift: %w", err)
	}
	if sub.ExtraAction {
		if _, err := s.AddHarm(a, sub.AgentID, sub.ExtraActionSuit, "S", snap.Limits.Downtime.ExtraActionCostSpiritualHarm, Opts{Reason: "downtime: a 3rd action"}); err != nil {
			return fmt.Errorf("extra action cost: %w", err)
		}
	}
	for i, act := range sub.Actions {
		if err := s.applyDowntimeAction(a, sub.AgentID, act, snap, o); err != nil {
			return fmt.Errorf("action %d (%s): %w", i+1, act.Kind, err)
		}
	}
	now := time.Now()
	return s.DB.Model(sub).Updates(map[string]any{"status": DowntimeStatusApproved, "seer_note": note, "decided_at": &now}).Error
}

func (s *Service) applyDowntimeAction(a Actor, agentID uint, act db.DowntimeAction, snap *gamedata.Snapshot, o Opts) error {
	l := snap.Limits.Downtime
	switch act.Kind {
	case "heal":
		_, _, err := s.Heal(a, agentID, act.HarmType, min2(act.Amount, l.HealMax), act.Suit, withNote(o, act.Note))
		return err
	case "train":
		return s.applyTrain(a, agentID, act.Track, l.TrainXP, l.TrainSegments, withNote(o, act.Note))
	case "reflect":
		return s.reflectTrack(a, agentID, act.Trait, act.TrackDelta, withNote(o, act.Note))
	case "new_contact":
		c := &db.Contact{AgentID: agentID, Kind: "Dioscorian", Name: act.ContactName, Card: act.ContactCard,
			Land: act.District, Description: act.ContactDescription, Affection: 1}
		return s.Create(a, "contact", c, withNote(o, act.Note))
	case "visit_contact":
		return s.applyVisit(a, agentID, act, snap, o)
	case "prepare":
		if act.ClockID != 0 && act.Segments != 0 {
			_, _, err := s.TickClock(a, act.ClockID, act.Segments, withNote(o, act.Note))
			return err
		}
		return nil // narrative only; nothing mechanical to apply
	}
	return fmt.Errorf("unknown kind %q", act.Kind)
}

func (s *Service) applyTrain(a Actor, agentID uint, track string, xp, segments int, o Opts) error {
	if school, ok := strings.CutPrefix(track, "proficiency:"); ok {
		return s.trainProficiency(a, agentID, school, segments, o)
	}
	_, _, err := s.AwardXP(a, agentID, track, xp, o)
	return err
}

// reflectTrack moves the Agent's burden or ideal track by delta (p. 68), clamped to its
// range; Update() then still catches anything that clamping missed.
func (s *Service) reflectTrack(a Actor, agentID uint, trait string, delta int, o Opts) error {
	ag, err := s.Agent(a, agentID)
	if err != nil {
		return err
	}
	snap, err := s.snap()
	if err != nil {
		return err
	}
	field, cur, limit := "burden_track", ag.BurdenTrack, snap.Limits.Agent.BurdenTrack
	if trait == "ideal" {
		field, cur, limit = "ideal_track", ag.IdealTrack, snap.Limits.Agent.IdealTrack
	}
	next := max(limit.Min, min2(cur+delta, limit.Max))
	_, err = s.Update(a, "agent", agentID, Patch{field: raw(next)}, o)
	return err
}

func (s *Service) trainProficiency(a Actor, agentID uint, school string, add int, o Opts) error {
	ag, err := s.Agent(a, agentID)
	if err != nil {
		return err
	}
	profs := make([]db.AgentProficiency, len(ag.Proficiencies))
	copy(profs, ag.Proficiencies)
	found := false
	for i := range profs {
		if strings.EqualFold(profs[i].School, school) {
			profs[i].Segments = min2(profs[i].Segments+add, 6)
			found = true
		}
	}
	if !found {
		return fmt.Errorf("no proficiency %q on the sheet", school)
	}
	_, err = s.Update(a, "agent", agentID, Patch{"proficiencies": raw(profs)}, o)
	return err
}

// applyVisit clears distance, adds affection, then applies the chosen activity's normal effect.
// The higher-affection contact bonuses (pp. 70-71) are the Seer's call on amount, since they
// depend on the visit's story; this applies the base effect and leaves room for the Seer to add
// more with the ordinary tools (another Heal/AwardXP/TickClock, or an override) if the bonus
// applies.
func (s *Service) applyVisit(a Actor, agentID uint, act db.DowntimeAction, snap *gamedata.Snapshot, o Opts) error {
	c, err := s.Get(a, "contact", act.ContactID)
	if err != nil {
		return err
	}
	contact := c.(*db.Contact)
	gain := 1
	if act.Activity == "heart_to_heart" {
		gain = 2
	}
	aff := min2(contact.Affection+gain, snap.Limits.Contact.Affection.Max)
	if _, err := s.Update(a, "contact", act.ContactID, Patch{"distance": raw(0), "affection": raw(aff)}, withNote(o, "downtime: visited "+contact.Name)); err != nil {
		return err
	}
	switch act.Activity {
	case "heal":
		_, _, err := s.Heal(a, agentID, act.HarmType, min2(act.Amount, snap.Limits.Downtime.HealMax), act.Suit, withNote(o, act.Note))
		return err
	case "train":
		return s.applyTrain(a, agentID, act.Track, snap.Limits.Downtime.TrainXP, snap.Limits.Downtime.TrainSegments, withNote(o, act.Note))
	case "reflect":
		return s.reflectTrack(a, agentID, act.Trait, act.TrackDelta, withNote(o, act.Note))
	case "heart_to_heart":
		if _, _, err := s.Heal(a, agentID, "T", 1, "", withNote(o, "downtime: heart-to-heart")); err != nil {
			return err
		}
		_, err := s.AddHarm(a, agentID, orDefaultSuit(act.Suit), "S", 2, withNote(o, "downtime: heart-to-heart cost"))
		return err
	}
	return nil
}

func withNote(o Opts, note string) Opts {
	if note != "" {
		o.Reason = o.Reason + ": " + note
	}
	return o
}

func orDefaultSuit(s string) string {
	if s == "" {
		return "Cups"
	}
	return s
}

func min2(a, b int) int {
	if a < b {
		return a
	}
	return b
}
