package web

import (
	"encoding/json"
	"fmt"
	"net/url"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/fwump38/hidden-isle-app/internal/campaign"
	"github.com/fwump38/hidden-isle-app/internal/db"
)

// Form conventions, shared by every edit form:
//
//	set.<field>=value   set a field (text, number, checkbox "on", date, list as one item per line)
//	inc.<field>=±n      add to a number field
//	why=…               the reason, stored on the change log
//	override=on         the Seer breaks a limit on purpose (needs a why)
//
// recordPatch turns such a form into a patch for any record type, by the target field's Go type.
func recordPatch(obj any, form url.Values) (campaign.Patch, error) {
	v := reflect.ValueOf(obj).Elem()
	t := v.Type()
	fieldIndex := map[string]int{}
	for i := 0; i < t.NumField(); i++ {
		name := strings.Split(t.Field(i).Tag.Get("json"), ",")[0]
		if name != "" && name != "-" {
			fieldIndex[name] = i
		}
	}
	p := campaign.Patch{}
	for key, vals := range form {
		op, field, ok := strings.Cut(key, ".")
		if !ok || (op != "set" && op != "inc") {
			continue
		}
		i, ok := fieldIndex[field]
		if !ok {
			return nil, fmt.Errorf("unknown field %q", field)
		}
		fv, ft := v.Field(i), t.Field(i).Type
		raw := strings.TrimSpace(vals[len(vals)-1]) // checkboxes send a hidden "" then "on"
		if op == "inc" {
			n, err := strconv.Atoi(raw)
			if err != nil || fv.Kind() != reflect.Int {
				return nil, fmt.Errorf("%s: can't add %q", field, raw)
			}
			p[field] = mustJSON(int(fv.Int()) + n)
			continue
		}
		val, err := parseValue(ft, raw)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", field, err)
		}
		p[field] = mustJSON(val)
	}
	return p, nil
}

func parseValue(ft reflect.Type, raw string) (any, error) {
	switch {
	case ft.Kind() == reflect.String: // includes db.Visibility
		return raw, nil
	case ft.Kind() == reflect.Int:
		if raw == "" {
			return 0, nil
		}
		return strconv.Atoi(raw)
	case ft.Kind() == reflect.Bool:
		return raw == "on" || raw == "true" || raw == "1", nil
	case ft == reflect.TypeOf((*uint)(nil)):
		if raw == "" || raw == "0" {
			return nil, nil
		}
		n, err := strconv.ParseUint(raw, 10, 64)
		return uint(n), err
	case ft == reflect.TypeOf((*time.Time)(nil)):
		if raw == "" {
			return nil, nil
		}
		d, err := time.Parse("2006-01-02", raw)
		return d, err
	case ft == reflect.TypeOf([]string(nil)):
		return splitList(raw), nil
	}
	return nil, fmt.Errorf("can't be set from a form")
}

// splitList reads one item per line (or comma-separated on a single line).
func splitList(raw string) []string {
	sep := "\n"
	if !strings.Contains(raw, "\n") {
		sep = ","
	}
	var out []string
	for _, s := range strings.Split(raw, sep) {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

// agentPatch handles the Agent sheet's structured fields on top of recordPatch:
//
//	skill.<Skill>=n                    set a skill's points
//	fourth=<Skill> (repeated)          the skills with the 4th pip unlocked (send fourth_present=1)
//	harm.<Suit>.0 / harm.<Suit>.1      a harm box: "", P, S or T
//	ability.add=<id>                   add a class ability
//	ability.custom_name / custom_text  add a custom ability
//	ability.remove=<index>
//	prof.add=<school>, prof.rank       add a proficiency
//	prof.<i>.segments / .rank / .boxes edit one; prof.remove=<i>
//	item.toggle=<name>                 pull (or put back) an item this scenario
//	item.custom=<name>                 pull a custom item
//	items.reset=1                      new scenario: clear pulled items and load
func agentPatch(ag *db.Agent, form url.Values) (campaign.Patch, error) {
	p, err := recordPatch(ag, form)
	if err != nil {
		return nil, err
	}
	cur := *ag

	skills := copyMap(cur.Skills)
	skillsChanged := false
	for key, vals := range form {
		if name, ok := strings.CutPrefix(key, "skill."); ok {
			n, err := strconv.Atoi(vals[len(vals)-1])
			if err != nil {
				return nil, fmt.Errorf("skill %s: %w", name, err)
			}
			skills[name] = n
			skillsChanged = true
		}
	}
	if skillsChanged {
		p["skills"] = mustJSON(skills)
	}
	if form.Get("fourth_present") != "" {
		p["unlocked_fourth"] = mustJSON(form["fourth"])
	}

	suits := map[string]bool{}
	for key := range form {
		if rest, ok := strings.CutPrefix(key, "harm."); ok {
			suit, _, _ := strings.Cut(rest, ".")
			suits[suit] = true
		}
	}
	if len(suits) > 0 {
		harm := map[string][]string{}
		for k, v := range cur.Harm {
			harm[k] = slices.Clone(v)
		}
		for suit := range suits {
			var marks []string
			for _, slot := range []string{"0", "1"} {
				if m := strings.TrimSpace(form.Get("harm." + suit + "." + slot)); m != "" {
					marks = append(marks, m)
				}
			}
			harm[suit] = marks
		}
		p["harm"] = mustJSON(harm)
	}

	abilities := slices.Clone(cur.Abilities)
	abChanged := false
	if id := form.Get("ability.add"); id != "" {
		abilities = append(abilities, db.AgentAbility{ID: id, Source: "class"})
		abChanged = true
	}
	if name := strings.TrimSpace(form.Get("ability.custom_name")); name != "" {
		abilities = append(abilities, db.AgentAbility{Name: name, Text: strings.TrimSpace(form.Get("ability.custom_text")),
			Source: orDefault(form.Get("ability.source"), "other")})
		abChanged = true
	}
	if s := form.Get("ability.remove"); s != "" {
		i, err := strconv.Atoi(s)
		if err != nil || i < 0 || i >= len(abilities) {
			return nil, fmt.Errorf("no ability %q", s)
		}
		abilities = slices.Delete(abilities, i, i+1)
		abChanged = true
	}
	if abChanged {
		p["abilities"] = mustJSON(abilities)
	}

	profs := slices.Clone(cur.Proficiencies)
	profChanged := false
	if school := strings.TrimSpace(form.Get("prof.add")); school != "" {
		rank := orDefault(form.Get("prof.rank"), "Novice")
		boxes := map[string]int{"Novice": 0, "Adept": 1, "Master": 3}[rank]
		profs = append(profs, db.AgentProficiency{School: school, Rank: rank, Boxes: boxes})
		profChanged = true
	}
	for i := range profs {
		pre := fmt.Sprintf("prof.%d.", i)
		if v := form.Get(pre + "segments"); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil {
				return nil, err
			}
			profs[i].Segments = n
			profChanged = true
		}
		if v := form.Get(pre + "inc"); v != "" {
			n, _ := strconv.Atoi(v)
			profs[i].Segments += n
			profChanged = true
		}
		if v := form.Get(pre + "rank"); v != "" {
			profs[i].Rank = v
			profChanged = true
		}
		if v := form.Get(pre + "boxes"); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil {
				return nil, err
			}
			profs[i].Boxes = n
			profChanged = true
		}
	}
	if s := form.Get("prof.remove"); s != "" {
		i, err := strconv.Atoi(s)
		if err != nil || i < 0 || i >= len(profs) {
			return nil, fmt.Errorf("no proficiency %q", s)
		}
		profs = slices.Delete(profs, i, i+1)
		profChanged = true
	}
	if profChanged {
		p["proficiencies"] = mustJSON(profs)
	}

	items, load := slices.Clone(cur.Items), cur.LoadUsed
	itemsChanged := false
	pull := func(name string, singleUse bool) {
		if i := slices.IndexFunc(items, func(it db.AgentItem) bool { return it.Name == name }); i >= 0 {
			items = slices.Delete(items, i, i+1)
			load--
		} else {
			items = append(items, db.AgentItem{Name: name, SingleUse: singleUse, Used: true})
			load++
		}
		itemsChanged = true
	}
	if name := form.Get("item.toggle"); name != "" {
		pull(name, form.Get("item.single_use") == "1")
	}
	if name := strings.TrimSpace(form.Get("item.custom")); name != "" {
		pull(name, false)
	}
	if form.Get("items.reset") != "" {
		items, load, itemsChanged = nil, 0, true
	}
	if itemsChanged {
		p["items"] = mustJSON(items)
		p["load_used"] = mustJSON(load)
	}
	return p, nil
}

func copyMap(m map[string]int) map[string]int {
	out := make(map[string]int, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func orDefault(s, def string) string {
	if strings.TrimSpace(s) == "" {
		return def
	}
	return s
}

// writeOpts reads the why/override fields shared by every edit form.
func writeOpts(form url.Values) campaign.Opts {
	return campaign.Opts{Reason: strings.TrimSpace(form.Get("why")), Override: form.Get("override") == "on"}
}
