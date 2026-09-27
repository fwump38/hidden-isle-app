package gamedata

// Range is an inclusive numeric limit with the page it comes from.
type Range struct {
	Min  int    `yaml:"min"`
	Max  int    `yaml:"max"`
	Page string `yaml:"page"`
}

// Limits is the typed part of data/limits.yaml the app validates against.
type Limits struct {
	Agent struct {
		BurdenTrack Range `yaml:"burden_track"`
		IdealTrack  Range `yaml:"ideal_track"`
		SuitXP      Range `yaml:"suit_xp"`
		AbilityXP   Range `yaml:"ability_xp"`
		LoadUsed    Range `yaml:"load_used"`
		Skill       struct {
			Min         int    `yaml:"min"`
			Max         int    `yaml:"max"`
			MaxUnlocked int    `yaml:"max_unlocked"`
			Page        string `yaml:"page"`
		} `yaml:"skill"`
		HarmPerSuit struct {
			Max   int      `yaml:"max"`
			Types []string `yaml:"types"`
			Page  string   `yaml:"page"`
		} `yaml:"harm_per_suit"`
		Virtues struct {
			Max  int    `yaml:"max"`
			Page string `yaml:"page"`
		} `yaml:"virtues"`
		ProficiencyClock struct {
			Segments int      `yaml:"segments"`
			Ranks    []string `yaml:"ranks"`
			Page     string   `yaml:"page"`
		} `yaml:"proficiency_clock"`
	} `yaml:"agent"`
	Creation struct {
		Page                       string `yaml:"page"`
		PrefilledPoints            int    `yaml:"prefilled_points"`
		CoreSelfPoints             int    `yaml:"core_self_points"`
		FreePoints                 int    `yaml:"free_points"`
		TotalPoints                int    `yaml:"total_points"`
		MaxSkill                   int    `yaml:"max_skill"`
		Abilities                  int    `yaml:"abilities"`
		AdeptProficiencies         int    `yaml:"adept_proficiencies"`
		HomelandContactAffection   []int  `yaml:"homeland_contact_affection"`
		DioscorianContactAffection int    `yaml:"dioscorian_contact_affection"`
		Solo                       struct {
			Page                      string `yaml:"page"`
			TotalPoints               int    `yaml:"total_points"`
			MaxSkill                  int    `yaml:"max_skill"`
			MaxSkillsAt3              int    `yaml:"max_skills_at_3"`
			OriginScenarioBonusPoints int    `yaml:"origin_scenario_bonus_points"`
		} `yaml:"solo"`
	} `yaml:"creation"`
	Contact struct {
		Affection Range `yaml:"affection"`
		Distance  Range `yaml:"distance"`
	} `yaml:"contact"`
	Clock struct {
		Segments []int  `yaml:"segments"`
		Page     string `yaml:"page"`
	} `yaml:"clock"`
	Adversary struct {
		Progress struct {
			Min  int    `yaml:"min"`
			Page string `yaml:"page"`
		} `yaml:"progress"`
		Sections int `yaml:"sections"`
	} `yaml:"adversary"`
}
