package gamedata

// Fixture returns a small, made-up but structurally complete snapshot for tests. It contains
// no text from the books: names are real skill/suit names, everything else is placeholder.
func Fixture() *Snapshot {
	s := &Snapshot{ID: "fixture", Raw: map[string]map[string]any{
		"setting": {"proficiencies": map[string]any{"schools": []any{map[string]any{"name": "Illusion"}, map[string]any{"name": "Mentalism"}}}},
	}}
	for _, suit := range []struct {
		name   string
		skills []string
	}{{"Swords", []string{"Skirmish", "Convince", "Study"}}, {"Wands", []string{"Unleash", "Perform", "Channel"}},
		{"Cups", []string{"Slip", "Soothe", "Mingle"}}, {"Pentacles", []string{"Finesse", "Bargain", "Survey"}}} {
		for _, sk := range suit.skills {
			s.Skills.Skills = append(s.Skills.Skills, Skill{ID: sk, Name: sk, Suit: suit.name, SheetDescription: "test"})
		}
		for _, rank := range []string{"Page", "Knight"} {
			s.Cards.Vision = append(s.Cards.Vision, VisionCard{ID: rank + "-" + suit.name, Name: rank + " of " + suit.name, Arcana: "court", Suit: suit.name, Rank: rank})
		}
		s.Cards.Pips = append(s.Cards.Pips, PipCard{ID: "ace-" + suit.name, Name: "Ace of " + suit.name, Suit: suit.name, Rank: "Ace", ChallengeValue: 11, FateValue: 1})
	}
	s.Classes.CommonItems = []Item{{Name: "Rope"}, {Name: "Signal flare", SingleUse: true, RulebookDescription: true, Description: "test flare"}}
	s.Classes.Classes = []Class{
		{ID: "prowler", Name: "Prowler", Guild: "Test Guild A", AbilityXPTrack: "Track A", Motto: "Test motto A.",
			PrefilledSkills: map[string]int{"Slip": 2, "Finesse": 1},
			Abilities:       []Ability{{ID: "wisp", Name: "WISP", Text: "Test ability text.", Page: 53}, {ID: "burglar", Name: "BURGLAR", Text: "Test.", SheetText: "Sheet test.", Page: 53}},
			Items:           []Item{{Name: "Lockpick"}}},
		{ID: "occultist", Name: "Occultist", Guild: "Test Guild B", AbilityXPTrack: "Track B", Motto: "Test motto B.",
			PrefilledSkills: map[string]int{"Unleash": 1, "Channel": 2}, StartsWithAdeptProficiency: true,
			Abilities: []Ability{{ID: "evil-eye", Name: "EVIL EYE", Text: "Test.", Page: 45, Clock: &Clock{Segments: 3, Name: "Law of reflection"}}}},
	}
	s.Campaign = Campaign{
		AgentStatus: []string{"Active", "Resting", "Dead"}, ContactKind: []string{"Homeland", "Dioscorian", "Other"},
		SessionStatus: []string{"Prep", "Played"}, AdversaryStatus: []string{"Rumored", "Active", "Defeated"},
		ClockScope: []string{"Scenario", "Ability", "Campaign"}, ClockStatus: []string{"Running", "Filled", "Abandoned"},
		Territories: []string{"Dioscoria", "Venice"}, CampaignMode: []string{"group", "solitaire"},
		Visibility: []string{"seer", "party", "owner"},
	}
	s.Adventures.Adventures = []Adventure{{ID: "test-adventure", Title: "Test Adventure", Book: "Test"}}
	l := &s.Limits
	l.Agent.BurdenTrack = Range{0, 7, "p. 20"}
	l.Agent.IdealTrack = Range{0, 7, "p. 20"}
	l.Agent.SuitXP = Range{0, 7, "p. 25"}
	l.Agent.AbilityXP = Range{0, 7, "p. 25"}
	l.Agent.LoadUsed = Range{0, 5, "p. 27"}
	l.Agent.Skill.Max, l.Agent.Skill.MaxUnlocked, l.Agent.Skill.Page = 3, 4, "p. 25"
	l.Agent.HarmPerSuit.Max, l.Agent.HarmPerSuit.Types, l.Agent.HarmPerSuit.Page = 2, []string{"P", "S", "T"}, "p. 23"
	l.Agent.Virtues.Max, l.Agent.Virtues.Page = 3, "p. 14"
	l.Agent.ProficiencyClock.Segments, l.Agent.ProficiencyClock.Ranks = 6, []string{"Novice", "Adept", "Master"}
	l.Contact.Affection = Range{0, 6, "p. 66"}
	l.Contact.Distance = Range{0, 3, "p. 66"}
	l.Clock.Segments, l.Clock.Page = []int{3, 4, 6, 8}, "p. 86"
	l.Adversary.Progress.Page = "p. 81"
	return s
}
