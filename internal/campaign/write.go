package campaign

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/fwump38/hidden-isle-app/internal/db"
	"github.com/fwump38/hidden-isle-app/internal/gamedata"
)

// Opts carries the why and the context of a change.
type Opts struct {
	Reason    string
	SessionID *uint
	// Override lets the Seer break a rules limit on purpose; the event is flagged and a house
	// ruling is recorded with the reason.
	Override bool
}

// Patch sets whole fields by their JSON name, e.g. {"burden_track": 3, "harm": {"Cups": ["P"]}}.
type Patch map[string]json.RawMessage

// Create inserts obj (its CampaignID must be set) and logs it.
func (s *Service) Create(a Actor, kindName string, obj any, o Opts) error {
	k, err := kindOf(kindName)
	if err != nil {
		return err
	}
	snap, err := s.snap()
	if err != nil {
		return err
	}
	return s.DB.Transaction(func(tx *gorm.DB) error {
		cid := k.campaign(obj)
		if kindName != "campaign" {
			if err := s.canView(tx, a, cid); err != nil {
				return err
			}
		}
		if err := s.checkWrite(tx, k, a, obj); err != nil {
			return err
		}
		if err := s.checkValid(tx, k, a, obj, snap, o); err != nil {
			return err
		}
		if err := tx.Create(obj).Error; err != nil {
			return err
		}
		full, _ := json.Marshal(obj)
		_, err := s.record(tx, a, k, obj, "create", nil, full, o)
		return err
	})
}

// Update applies patch to one record, validates it, saves it and logs the changed fields.
func (s *Service) Update(a Actor, kindName string, id uint, patch Patch, o Opts) (any, error) {
	k, err := kindOf(kindName)
	if err != nil {
		return nil, err
	}
	snap, err := s.snap()
	if err != nil {
		return nil, err
	}
	obj := k.new()
	err = s.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.First(obj, id).Error; err != nil {
			return notFound(err)
		}
		if err := s.canView(tx, a, k.campaign(obj)); err != nil {
			return err
		}
		if !k.canRead(a, obj) {
			return ErrNotFound
		}
		if err := s.checkWrite(tx, k, a, obj); err != nil {
			return err
		}
		for f := range patch {
			if slices.Contains(lockedFields, f) {
				return fmt.Errorf("%s can't be changed", f)
			}
			if !a.IsSeer() && (slices.Contains(k.seerOnly, f) || slices.Contains(k.secret, f)) {
				return fmt.Errorf("only the Seer can change %s: %w", f, ErrForbidden)
			}
		}
		before := fields(obj)
		if err := applyPatch(obj, patch); err != nil {
			return err
		}
		if err := s.checkWrite(tx, k, a, obj); err != nil { // e.g. a player can't hand an Agent away
			return err
		}
		if err := s.checkValid(tx, k, a, obj, snap, o); err != nil {
			return err
		}
		changes := diff(before, fields(obj))
		if len(changes) == 0 {
			return nil
		}
		if ag, ok := obj.(*db.Agent); ok {
			ag.Version++
		}
		if err := tx.Save(obj).Error; err != nil {
			return err
		}
		_, err := s.record(tx, a, k, obj, "update", changes, nil, o)
		return err
	})
	if err != nil {
		return nil, err
	}
	return s.redact(k, a, obj), nil
}

// Delete removes one record and logs a snapshot, so it can be undone.
func (s *Service) Delete(a Actor, kindName string, id uint, o Opts) error {
	k, err := kindOf(kindName)
	if err != nil {
		return err
	}
	return s.DB.Transaction(func(tx *gorm.DB) error {
		obj := k.new()
		if err := tx.First(obj, id).Error; err != nil {
			return notFound(err)
		}
		if err := s.canView(tx, a, k.campaign(obj)); err != nil {
			return err
		}
		if (k.seerDel || k.seerWrite) && !a.IsSeer() {
			return ErrForbidden
		}
		if err := s.checkWrite(tx, k, a, obj); err != nil {
			return err
		}
		full, _ := json.Marshal(obj)
		if err := tx.Delete(obj).Error; err != nil {
			return err
		}
		_, err := s.record(tx, a, k, obj, "delete", nil, full, o)
		return err
	})
}

// Revert undoes one event. The Seer may undo anything; a player may undo their own changes.
func (s *Service) Revert(a Actor, eventID uint, reason string) error {
	snap, err := s.snap()
	if err != nil {
		return err
	}
	return s.DB.Transaction(func(tx *gorm.DB) error {
		var ev db.Event
		if err := tx.First(&ev, eventID).Error; err != nil {
			return notFound(err)
		}
		if err := s.canView(tx, a, ev.CampaignID); err != nil {
			return err
		}
		if !a.IsSeer() && (ev.ActorID == nil || *ev.ActorID != a.User.ID) {
			return ErrForbidden
		}
		if ev.RevertedBy != nil {
			return errors.New("that change was already undone")
		}
		k, err := kindOf(ev.EntityType)
		if err != nil {
			return err
		}
		o := Opts{Reason: reason, Override: a.IsSeer()}
		if o.Reason == "" {
			o.Reason = fmt.Sprintf("undo #%d", ev.ID)
		}
		obj := k.new()
		var undo *db.Event
		switch ev.Action {
		case "update", "revert":
			if err := tx.First(obj, ev.EntityID).Error; err != nil {
				return notFound(err)
			}
			if err := s.checkWrite(tx, k, a, obj); err != nil {
				return err
			}
			cur := fields(obj)
			var conflicts []string
			back := Patch{}
			for _, c := range ev.Changes {
				if !jsonEqual(cur[c.Field], c.To) {
					conflicts = append(conflicts, c.Field)
				}
				back[c.Field] = c.From
			}
			if len(conflicts) > 0 {
				return &ConflictError{Fields: conflicts}
			}
			before := fields(obj)
			if err := applyPatch(obj, back); err != nil {
				return err
			}
			if err := s.checkValid(tx, k, a, obj, snap, o); err != nil {
				return err
			}
			if err := tx.Save(obj).Error; err != nil {
				return err
			}
			undo, err = s.record(tx, a, k, obj, "revert", diff(before, fields(obj)), nil, o)
		case "create":
			if err := tx.First(obj, ev.EntityID).Error; err != nil {
				return notFound(err)
			}
			if err := s.checkWrite(tx, k, a, obj); err != nil {
				return err
			}
			full, _ := json.Marshal(obj)
			if err := tx.Delete(obj).Error; err != nil {
				return err
			}
			undo, err = s.record(tx, a, k, obj, "delete", nil, full, o)
		case "delete":
			if len(ev.Snapshot) == 0 { // the public copy of a split event: use the Seer copy
				var sib db.Event
				if err := tx.Where("entity_type = ? AND entity_id = ? AND action = ? AND created_at = ? AND snapshot IS NOT NULL",
					ev.EntityType, ev.EntityID, ev.Action, ev.CreatedAt).First(&sib).Error; err != nil {
					return errors.New("can't restore: no snapshot recorded")
				}
				ev.Snapshot = sib.Snapshot
			}
			if err := json.Unmarshal(ev.Snapshot, obj); err != nil {
				return fmt.Errorf("can't restore: %w", err)
			}
			if err := s.checkWrite(tx, k, a, obj); err != nil {
				return err
			}
			if err := tx.Create(obj).Error; err != nil {
				return err
			}
			full, _ := json.Marshal(obj)
			undo, err = s.record(tx, a, k, obj, "create", nil, full, o)
		default:
			return fmt.Errorf("can't undo a %s", ev.Action)
		}
		if err != nil {
			return err
		}
		undo.RevertOf = &ev.ID
		if err := tx.Model(undo).Update("revert_of", ev.ID).Error; err != nil {
			return err
		}
		// Mark the event, and its Seer/public twin if it was split, as undone.
		return tx.Model(&db.Event{}).Where("id = ? OR (entity_type = ? AND entity_id = ? AND action = ? AND created_at = ? AND reverted_by IS NULL)",
			ev.ID, ev.EntityType, ev.EntityID, ev.Action, ev.CreatedAt).Update("reverted_by", undo.ID).Error
	})
}

func (s *Service) checkWrite(tx *gorm.DB, k *kind, a Actor, obj any) error {
	if a.User == nil {
		return ErrForbidden
	}
	if k.seerWrite && !a.IsSeer() {
		return ErrForbidden
	}
	if k.canWrite != nil {
		return k.canWrite(s, tx, a, obj)
	}
	if !a.IsSeer() {
		return ErrForbidden
	}
	return nil
}

// checkValid validates obj. The Seer may override with a reason, which records a house ruling.
func (s *Service) checkValid(tx *gorm.DB, k *kind, a Actor, obj any, snap *gamedata.Snapshot, o Opts) error {
	ps := k.validate(snap, obj)
	if len(ps) == 0 {
		return nil
	}
	if !(o.Override && a.IsSeer()) {
		return problems(ps)
	}
	if strings.TrimSpace(o.Reason) == "" {
		return errors.New("breaking a rules limit needs a reason; it's recorded as a house ruling")
	}
	hr := db.HouseRuling{CampaignID: k.campaign(obj), Ruling: fmt.Sprintf("%s (%s): %s", k.display(obj), strings.Join(ps, "; "), o.Reason)}
	return tx.Create(&hr).Error
}

// record writes the event(s) for a change. Changes to Seer-only fields go in a separate event
// visible only to the Seer. It returns the first event written.
func (s *Service) record(tx *gorm.DB, a Actor, k *kind, obj any, action string, changes []db.Change, full json.RawMessage, o Opts) (*db.Event, error) {
	vis, owner := k.vis(obj)
	base := db.Event{
		CampaignID: k.campaign(obj), SessionID: o.SessionID, ActorID: a.id(), ActorName: a.name(), Via: a.Via,
		EntityType: k.name, EntityID: entityID(obj), EntityName: k.display(obj), Action: action,
		Reason: o.Reason, Override: o.Override && a.IsSeer(), Visibility: vis, OwnerID: owner, Snapshot: full,
		CreatedAt: time.Now(),
	}
	if base.Via == "" {
		base.Via = "web"
	}
	var public, secret []db.Change
	for _, c := range changes {
		if slices.Contains(k.secret, c.Field) {
			secret = append(secret, c)
		} else {
			public = append(public, c)
		}
	}
	var events []*db.Event
	if len(k.secret) > 0 && full != nil && vis != db.VisSeer {
		// Create/delete snapshots contain secrets: keep the snapshot on a Seer-only event.
		pub := base
		pub.Snapshot = nil
		pub.Summary = fmt.Sprintf("%s %sd", base.EntityName, action)
		sec := base
		sec.Visibility = db.VisSeer
		sec.Summary = pub.Summary + " (Seer copy)"
		events = append(events, &pub, &sec)
	} else if action == "create" || action == "delete" {
		ev := base
		ev.Summary = fmt.Sprintf("%s %sd", base.EntityName, action)
		events = append(events, &ev)
	} else {
		if len(public) > 0 {
			ev := base
			ev.Changes = public
			ev.Summary = summarize(base.EntityName, public)
			events = append(events, &ev)
		}
		if len(secret) > 0 {
			ev := base
			ev.Changes = secret
			ev.Visibility = db.VisSeer
			ev.Summary = summarize(base.EntityName, secret)
			events = append(events, &ev)
		}
	}
	for _, ev := range events {
		if err := tx.Create(ev).Error; err != nil {
			return nil, err
		}
	}
	return events[0], nil
}

func entityID(obj any) uint {
	return uint(reflect.ValueOf(obj).Elem().FieldByName("ID").Uint())
}

// fields returns a record's JSON fields, keyed by name.
func fields(obj any) map[string]json.RawMessage {
	b, _ := json.Marshal(obj)
	m := map[string]json.RawMessage{}
	_ = json.Unmarshal(b, &m)
	return m
}

var ignoredInDiff = []string{"updated_at", "created_at"}

func diff(before, after map[string]json.RawMessage) []db.Change {
	var out []db.Change
	for f, to := range after {
		if slices.Contains(ignoredInDiff, f) {
			continue
		}
		if from := before[f]; !jsonEqual(from, to) {
			out = append(out, db.Change{Field: f, From: from, To: to})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Field < out[j].Field })
	return out
}

// jsonEqual compares JSON values, treating null, [] and {} as the same empty value.
func jsonEqual(a, b json.RawMessage) bool {
	norm := func(r json.RawMessage) string {
		var v any
		if len(r) == 0 || json.Unmarshal(r, &v) != nil {
			return "null"
		}
		switch x := v.(type) {
		case []any:
			if len(x) == 0 {
				return "null"
			}
		case map[string]any:
			if len(x) == 0 {
				return "null"
			}
		}
		out, _ := json.Marshal(v)
		return string(out)
	}
	return norm(a) == norm(b)
}

// applyPatch replaces whole fields: each named field is zeroed first, so maps and lists are
// replaced rather than merged.
func applyPatch(obj any, p Patch) error {
	v := reflect.ValueOf(obj).Elem()
	t := v.Type()
	byJSON := map[string]int{}
	for i := 0; i < t.NumField(); i++ {
		name := strings.Split(t.Field(i).Tag.Get("json"), ",")[0]
		if name != "" && name != "-" {
			byJSON[name] = i
		}
	}
	for f := range p {
		i, ok := byJSON[f]
		if !ok {
			return fmt.Errorf("unknown field %q", f)
		}
		v.Field(i).Set(reflect.Zero(v.Field(i).Type()))
	}
	b, err := json.Marshal(p)
	if err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	if err := dec.Decode(obj); err != nil {
		return fmt.Errorf("bad value: %w", err)
	}
	return nil
}

func summarize(name string, cs []db.Change) string {
	parts := make([]string, 0, len(cs))
	for _, c := range cs {
		parts = append(parts, describe(c))
	}
	return name + ": " + strings.Join(parts, "; ")
}

// describe renders one change for people: maps diff by key (skills, harm), lists of records
// by what was added or removed (abilities, items), and everything else as "from → to".
func describe(c db.Change) string {
	label := strings.ReplaceAll(c.Field, "_", " ")
	var fm, tm map[string]any
	fromErr, toErr := json.Unmarshal(c.From, &fm), json.Unmarshal(c.To, &tm)
	if fromErr == nil && toErr == nil {
		if fm != nil || tm != nil {
			keys := map[string]bool{}
			for k := range fm {
				keys[k] = true
			}
			for k := range tm {
				keys[k] = true
			}
			var ks []string
			for k := range keys {
				if !jsonEqual(mustMarshal(fm[k]), mustMarshal(tm[k])) {
					ks = append(ks, k)
				}
			}
			sort.Strings(ks)
			var out []string
			for _, k := range ks {
				out = append(out, fmt.Sprintf("%s %s → %s", k, brief(fm[k]), brief(tm[k])))
			}
			if len(out) > 0 {
				if c.Field == "skills" {
					return strings.Join(out, ", ")
				}
				return label + " " + strings.Join(out, ", ")
			}
		}
	}
	var fl, tl []any
	if json.Unmarshal(c.From, &fl) == nil && json.Unmarshal(c.To, &tl) == nil && (len(fl) > 0 || len(tl) > 0) {
		if _, isObj := first(fl, tl).(map[string]any); isObj {
			var added, removed []string
			fn, tn := names(fl), names(tl)
			for _, n := range tn {
				if !slices.Contains(fn, n) {
					added = append(added, "+ "+n)
				}
			}
			for _, n := range fn {
				if !slices.Contains(tn, n) {
					removed = append(removed, "− "+n)
				}
			}
			if len(added)+len(removed) > 0 {
				return label + " " + strings.Join(append(added, removed...), ", ")
			}
			return label + " changed"
		}
		return fmt.Sprintf("%s %s → %s", label, brief(fl), brief(tl))
	}
	return fmt.Sprintf("%s %s → %s", label, show(c.From), show(c.To))
}

func first(a, b []any) any {
	if len(a) > 0 {
		return a[0]
	}
	if len(b) > 0 {
		return b[0]
	}
	return nil
}

// names picks a readable name for each record in a list (name, id or school).
func names(list []any) []string {
	var out []string
	for _, v := range list {
		m, _ := v.(map[string]any)
		for _, k := range []string{"name", "id", "school"} {
			if s, ok := m[k].(string); ok && s != "" {
				out = append(out, s)
				break
			}
		}
	}
	return out
}

// brief renders a decoded JSON value compactly: lists as space-separated items.
func brief(v any) string {
	switch x := v.(type) {
	case nil:
		return "—"
	case []any:
		if len(x) == 0 {
			return "—"
		}
		parts := make([]string, len(x))
		for i, e := range x {
			parts[i] = brief(e)
		}
		return truncate(strings.Join(parts, " "), 60)
	case string:
		if x == "" {
			return "—"
		}
		return truncate(x, 40)
	case float64:
		return fmt.Sprint(x)
	}
	return truncate(string(mustMarshal(v)), 60)
}

func mustMarshal(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

func show(r json.RawMessage) string {
	if len(r) == 0 || string(r) == "null" || string(r) == `""` || string(r) == "[]" || string(r) == "{}" {
		return "—"
	}
	var s string
	if json.Unmarshal(r, &s) == nil {
		return truncate(s, 40)
	}
	return truncate(string(r), 60)
}

func truncate(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

// redact blanks Seer-only fields for players. It works on a copy.
func (s *Service) redact(k *kind, a Actor, obj any) any {
	if a.IsSeer() || len(k.secret) == 0 {
		return obj
	}
	cp := k.new()
	b, _ := json.Marshal(obj)
	_ = json.Unmarshal(b, cp)
	blank := Patch{}
	for _, f := range k.secret {
		blank[f] = json.RawMessage(`""`)
	}
	_ = applyPatch(cp, blank)
	return cp
}
