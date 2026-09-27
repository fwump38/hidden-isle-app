// Package creation is the deterministic "automatic" character-creation path (pp. 40-41): real
// card draws (internal/cards), the same rules-legal choices a player would make by hand, and no
// Claude involvement at all — it works even with the in-app chat off. A Seer or player reviews
// and edits the result afterward; nothing here is final until it's saved to the sheet.
package creation

import (
	"crypto/rand"
	"fmt"
	"math/big"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/fwump38/hidden-isle-app/internal/cards"
	"github.com/fwump38/hidden-isle-app/internal/gamedata"
	"github.com/fwump38/hidden-isle-app/internal/oracle"
)

// WhyReasons are the book's four example reasons for coming to Dioscoria (p. 41). The last one
// ties the generated Agent to Dioscoria itself rather than one of the five cities.
var WhyReasons = []string{
	"Fleeing famine, disaster or war",
	"Persecuted for how I live",
	"A magician seeking acceptance for my art",
	"Native-born, coming of age and ready to serve",
}

// cultures maps a region to a short demonym for the sheet's Culture field. Not book text —
// just a plain adjective, not a claim about anything in the rules.
var cultures = map[string]string{
	"London": "English", "Lisbon": "Portuguese", "Venice": "Venetian",
	"Konstantiniyye / Istanbul": "Ottoman", "Qazvin": "Persian", "Dioscoria": "Dioscorian",
}

// Contact is a homeland or Dioscorian contact, ready to create once the caller sets AgentID.
type Contact struct {
	Kind        string
	Name        string
	Card        string
	Land        string
	Description string
	Affection   int
}

// Result is everything Generate produced. Fields uses the same JSON field names as db.Agent
// (name, look, skills, …), so a caller can turn it into a campaign.Patch directly.
type Result struct {
	Fields   map[string]any
	Contacts []Contact
	Log      []string // a human-readable line per step, for the player or Seer to review
}

// Generate runs the full creation procedure once, using real draws and the rules data. class
// must be the Agent's already-chosen class (step 1 happens before this). solo applies the solo
// character's skill limits (p. 96).
func Generate(snap *gamedata.Snapshot, class *gamedata.Class, solo bool) (*Result, error) {
	if snap == nil {
		return nil, fmt.Errorf("rules data hasn't loaded yet")
	}
	if class == nil {
		return nil, fmt.Errorf("pick a class first")
	}
	g := &Result{Fields: map[string]any{}}
	l := snap.Limits

	childCard, err := drawOne(snap)
	if err != nil {
		return nil, err
	}
	childPhrase, err := pickOne(childCard.History)
	if err != nil {
		return nil, fmt.Errorf("%s has no Character History phrases", childCard.Name)
	}
	g.Fields["child_card"], g.Fields["child_phrase"] = childCard.Name, childPhrase
	g.Log = append(g.Log, fmt.Sprintf("Childhood: drew %s → \"%s\" (p. 40).", childCard.Name, childPhrase))

	adultCard, err := drawOne(snap)
	if err != nil {
		return nil, err
	}
	adultPhrase, err := pickOne(adultCard.History)
	if err != nil {
		return nil, fmt.Errorf("%s has no Character History phrases", adultCard.Name)
	}
	verb, err := pickOne([]string{"survived", "flourished"})
	if err != nil {
		return nil, err
	}
	g.Fields["adult_card"], g.Fields["adult_phrase"], g.Fields["adult_verb"] = adultCard.Name, adultPhrase, verb
	g.Log = append(g.Log, fmt.Sprintf("Adulthood: drew %s → %s by \"%s\" (p. 40).", adultCard.Name, verb, adultPhrase))

	burdenCard, err := drawOne(snap)
	if err != nil {
		return nil, err
	}
	burden, err := pickOne(burdenCard.Burdens)
	if err != nil {
		return nil, fmt.Errorf("%s has no Burdens words", burdenCard.Name)
	}
	g.Fields["burden_card"], g.Fields["burden"] = burdenCard.Name, burden
	g.Log = append(g.Log, fmt.Sprintf("Burden: drew %s → %q (p. 41).", burdenCard.Name, burden))

	idealCard, err := drawOne(snap)
	if err != nil {
		return nil, err
	}
	ideal, err := pickOne(idealCard.Ideals)
	if err != nil {
		return nil, fmt.Errorf("%s has no Ideals words", idealCard.Name)
	}
	g.Fields["ideal_card"], g.Fields["ideal"] = idealCard.Name, ideal
	g.Log = append(g.Log, fmt.Sprintf("Ideal: drew %s → %q (p. 41).", idealCard.Name, ideal))

	abilities, err := pickAbilities(class.Abilities, l.Creation.Abilities)
	if err != nil {
		return nil, err
	}
	var abilityFields []map[string]string
	var names []string
	for _, ab := range abilities {
		abilityFields = append(abilityFields, map[string]string{"id": ab.ID})
		names = append(names, ab.Name)
	}
	g.Fields["abilities"] = abilityFields
	g.Log = append(g.Log, fmt.Sprintf("Abilities: %s (p. 41).", strings.Join(names, ", ")))

	skills, err := allocateSkills(snap, class, l, solo)
	if err != nil {
		return nil, err
	}
	g.Fields["skills"] = skills
	g.Log = append(g.Log, fmt.Sprintf("Skills: %s (p. 41).", describeSkills(skills)))

	if class.StartsWithAdeptProficiency {
		school, err := pickProficiencySchool(snap)
		if err != nil {
			return nil, err
		}
		g.Fields["proficiencies"] = []map[string]any{{"school": school, "rank": "Adept", "boxes": 1}}
		g.Log = append(g.Log, fmt.Sprintf("Magic: %s at Adept (p. 41).", school))
	}

	why, err := pickOne(WhyReasons)
	if err != nil {
		return nil, err
	}
	g.Fields["why"] = why
	region := "Dioscoria"
	if !strings.HasPrefix(why, "Native-born") {
		regions, err := oracleRegions(snap)
		if err != nil {
			return nil, err
		}
		region, err = pickOne(regions)
		if err != nil {
			return nil, err
		}
	}
	name, err := regionName(snap, region)
	if err != nil {
		return nil, err
	}
	age, err := randRange(18, 55)
	if err != nil {
		return nil, err
	}
	g.Fields["name"] = name
	g.Fields["age"] = fmt.Sprint(age)
	g.Fields["culture"] = cultureOf(region)
	g.Fields["look"] = fmt.Sprintf("Draft — edit freely, or ask chat to flesh it out: something of %s (%s).", adultCard.Name, adultCard.Characters)
	g.Log = append(g.Log, fmt.Sprintf("Identity: %s, age %d, %s, from %s. Why Dioscoria: %s (p. 41).", name, age, g.Fields["culture"], region, why))

	homeland, err := makeContact(snap, "Homeland", region, l.Creation.HomelandContactAffection)
	if err != nil {
		return nil, err
	}
	dioscorian, err := makeContact(snap, "Dioscorian", "Dioscoria", []int{l.Creation.DioscorianContactAffection})
	if err != nil {
		return nil, err
	}
	g.Contacts = []Contact{homeland, dioscorian}
	g.Log = append(g.Log, fmt.Sprintf("Homeland contact: %s (%s), affection %d (p. 41).", homeland.Name, homeland.Card, homeland.Affection))
	g.Log = append(g.Log, fmt.Sprintf("Dioscorian contact: %s (%s), affection %d (p. 41).", dioscorian.Name, dioscorian.Card, dioscorian.Affection))

	return g, nil
}

// ---------------------------------------------------------------- drawing and picking

func drawOne(snap *gamedata.Snapshot) (gamedata.VisionCard, error) {
	hands, err := cards.Draw(snap, "vision", []cards.Request{{Count: 1}})
	if err != nil {
		return gamedata.VisionCard{}, err
	}
	return oracle.FindVision(snap, hands[0].Cards[0])
}

func drawN(snap *gamedata.Snapshot, n int) ([]gamedata.VisionCard, error) {
	hands, err := cards.Draw(snap, "vision", []cards.Request{{Count: n}})
	if err != nil {
		return nil, err
	}
	var out []gamedata.VisionCard
	for _, name := range hands[0].Cards {
		c, err := oracle.FindVision(snap, name)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}

// pickOne returns one random element of list, using OS randomness like every other draw here.
func pickOne[T any](list []T) (T, error) {
	var zero T
	if len(list) == 0 {
		return zero, fmt.Errorf("nothing to pick from")
	}
	n, err := rand.Int(rand.Reader, big.NewInt(int64(len(list))))
	if err != nil {
		return zero, err
	}
	return list[n.Int64()], nil
}

func randRange(min, max int) (int, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(int64(max-min+1)))
	if err != nil {
		return 0, err
	}
	return min + int(n.Int64()), nil
}

func pickAbilities(list []gamedata.Ability, n int) ([]gamedata.Ability, error) {
	if n > len(list) {
		n = len(list)
	}
	idx := make([]int, len(list))
	for i := range idx {
		idx[i] = i
	}
	for i := 0; i < n; i++ {
		j, err := rand.Int(rand.Reader, big.NewInt(int64(len(idx)-i)))
		if err != nil {
			return nil, err
		}
		k := i + int(j.Int64())
		idx[i], idx[k] = idx[k], idx[i]
	}
	var out []gamedata.Ability
	for i := 0; i < n; i++ {
		out = append(out, list[idx[i]])
	}
	return out, nil
}

// allocateSkills spreads the creation points (2 core-self-inspired + 2 free, p. 41) across the
// 12 skills: the "core self" points lean toward the class's own suits, the rest are open. Every
// skill respects the creation cap, except a solo character may push a few skills to 3 (p. 96).
func allocateSkills(snap *gamedata.Snapshot, class *gamedata.Class, l gamedata.Limits, solo bool) (map[string]int, error) {
	skills := map[string]int{}
	for sk, v := range class.PrefilledSkills {
		skills[sk] = v
	}
	total := l.Creation.TotalPoints
	baseCap := l.Creation.MaxSkill
	extraCap, extraSlots := baseCap, 0
	if solo {
		if l.Creation.Solo.TotalPoints > 0 {
			total = l.Creation.Solo.TotalPoints
		}
		if l.Creation.Solo.MaxSkill > 0 {
			extraCap = l.Creation.Solo.MaxSkill
		}
		extraSlots = l.Creation.Solo.MaxSkillsAt3
	}
	// The class's suits (from its pre-filled skills) get the "core self" weighting.
	suitOf := map[string]string{}
	for _, sk := range snap.Skills.Skills {
		suitOf[sk.Name] = sk.Suit
	}
	classSuits := map[string]bool{}
	for sk := range class.PrefilledSkills {
		classSuits[suitOf[sk]] = true
	}
	suits := map[string]bool{}
	for _, sk := range snap.Skills.Skills {
		if classSuits[sk.Suit] {
			suits[sk.Name] = true
		}
	}
	prefilled := 0
	for _, v := range skills {
		prefilled += v
	}
	toAdd := total - prefilled
	if toAdd < 0 {
		toAdd = 0
	}
	bumped := 0
	var all []string
	for _, sk := range snap.Skills.Skills {
		all = append(all, sk.Name)
	}
	for i := 0; i < toAdd; i++ {
		candidates := eligibleSkills(all, skills, baseCap, extraCap, extraSlots-bumped)
		if len(candidates) == 0 {
			return nil, fmt.Errorf("no skill left under the creation cap to place a point (need %d more)", toAdd-i)
		}
		weighted := candidates
		if i < l.Creation.CoreSelfPoints {
			// Core-self points: weight toward the class's own suits by listing them twice.
			for _, sk := range candidates {
				if suits[sk] {
					weighted = append(weighted, sk)
				}
			}
		}
		sk, err := pickOne(weighted)
		if err != nil {
			return nil, err
		}
		if skills[sk] == baseCap {
			bumped++
		}
		skills[sk]++
	}
	return skills, nil
}

func eligibleSkills(all []string, cur map[string]int, baseCap, extraCap, bumpsLeft int) []string {
	var out []string
	for _, sk := range all {
		v := cur[sk]
		if v < baseCap || (v == baseCap && bumpsLeft > 0 && extraCap > baseCap) {
			out = append(out, sk)
		}
	}
	return out
}

func describeSkills(skills map[string]int) string {
	var parts []string
	for sk, v := range skills {
		if v > 0 {
			parts = append(parts, fmt.Sprintf("%s %d", sk, v))
		}
	}
	return strings.Join(parts, ", ")
}

// ---------------------------------------------------------------- setting lookups

type schoolTable struct {
	Schools []struct {
		Name string `yaml:"name"`
	} `yaml:"schools"`
}

func pickProficiencySchool(snap *gamedata.Snapshot) (string, error) {
	var t schoolTable
	if err := remarshal(snap.Raw["setting"]["proficiencies"], &t); err != nil || len(t.Schools) == 0 {
		return "", fmt.Errorf("no magic schools in the rules data")
	}
	var names []string
	for _, s := range t.Schools {
		names = append(names, s.Name)
	}
	return pickOne(names)
}

func oracleRegions(snap *gamedata.Snapshot) ([]string, error) {
	t, err := oracle.Load(snap)
	if err != nil {
		return nil, err
	}
	regions := t.Regions()
	if len(regions) == 0 {
		return nil, fmt.Errorf("no regional name lists in the rules data")
	}
	return regions, nil
}

// regionName picks a full name for someone from region: a given name from the book's list (or
// the homebrew Dioscorian list), plus a homebrew surname or byname when one exists.
func regionName(snap *gamedata.Snapshot, region string) (string, error) {
	var given string
	if hb := snap.HomebrewRegion(region); hb != nil && len(hb.Given) > 0 {
		g, err := pickOne(hb.Given)
		if err != nil {
			return "", err
		}
		given = g
	} else {
		regions, err := oracleRegions(snap)
		if err != nil {
			return "", err
		}
		found := false
		for _, r := range regions {
			if r == region {
				found = true
			}
		}
		if !found {
			return "", fmt.Errorf("no names for region %q", region)
		}
		t, err := oracle.Load(snap)
		if err != nil {
			return "", err
		}
		for _, nl := range t.Names {
			if nl.Region == region {
				g, err := pickOne(nl.Names)
				if err != nil {
					return "", err
				}
				given = g
			}
		}
	}
	if given == "" {
		return "", fmt.Errorf("no given names for region %q", region)
	}
	full := given
	if hb := snap.HomebrewRegion(region); hb != nil {
		if len(hb.Before) > 0 {
			b, err := pickOne(hb.Before)
			if err == nil {
				full = b + " " + full
			}
		}
		if len(hb.After) > 0 {
			a, err := pickOne(hb.After)
			if err == nil {
				full = full + " " + a
			}
		}
	}
	return full, nil
}

func cultureOf(region string) string {
	if c, ok := cultures[region]; ok {
		return c
	}
	return region
}

func makeContact(snap *gamedata.Snapshot, kind, region string, affections []int) (Contact, error) {
	drawn, err := drawN(snap, 3)
	if err != nil {
		return Contact{}, err
	}
	card, err := pickOne(drawn)
	if err != nil {
		return Contact{}, err
	}
	name, err := regionName(snap, region)
	if err != nil {
		return Contact{}, err
	}
	affection, err := pickOne(affections)
	if err != nil {
		return Contact{}, err
	}
	land := region
	if kind == "Dioscorian" {
		if hb := snap.HomebrewRegion("Dioscoria"); hb != nil && len(hb.After) > 0 {
			a, err := pickOne(hb.After)
			if err == nil {
				land = strings.TrimPrefix(a, "of ")
			}
		}
	}
	desc := card.Characters
	if card.Meaning != "" {
		desc = card.Meaning + ": " + desc
	}
	return Contact{Kind: kind, Name: name, Card: card.Name, Land: land, Description: desc, Affection: affection}, nil
}

func remarshal(in any, out any) error {
	b, err := yaml.Marshal(in)
	if err != nil {
		return err
	}
	return yaml.Unmarshal(b, out)
}
