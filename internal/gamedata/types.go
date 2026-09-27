// Package gamedata syncs the rules repo (see its hidden-isle-data.yaml), validates it, and serves
// the last good snapshot to the rest of the app.
package gamedata

import (
	"strings"
	"time"
)

// SupportedSchemas lists the manifest schema_version values this build understands.
var SupportedSchemas = []int{1}

type Manifest struct {
	Version       string `yaml:"version"`
	SchemaVersion int    `yaml:"schema_version"`
	Data          struct {
		Generated map[string]string `yaml:"generated"`
		Hand      map[string]string `yaml:"hand"`
		Homebrew  map[string]string `yaml:"homebrew"` // not from the books; see Homebrew
	} `yaml:"data"`
	Text       []TextSource      `yaml:"text"`
	Prompts    string            `yaml:"prompts"` // folder of <skill>/SKILL.md files, served as MCP prompts
	Citations  map[string]string `yaml:"citations"`
	Precedence []string          `yaml:"precedence"`
}

// Audience says who may read a text source: "party" (players and Seer) or "seer" only.
type TextSource struct {
	Path     string `yaml:"path"`
	Audience string `yaml:"audience"`
	Role     string `yaml:"role"`
}

type VisionCard struct {
	ID                 string   `yaml:"id" json:"id"`
	Name               string   `yaml:"name" json:"name"`
	Numeral            string   `yaml:"numeral,omitempty" json:"numeral,omitempty"`
	Arcana             string   `yaml:"arcana" json:"arcana"`
	Suit               string   `yaml:"suit,omitempty" json:"suit,omitempty"`
	Rank               string   `yaml:"rank,omitempty" json:"rank,omitempty"`
	Page               string   `yaml:"page" json:"page"`
	Meaning            string   `yaml:"meaning" json:"meaning"`
	Characters         string   `yaml:"characters" json:"characters"`
	Locations          string   `yaml:"locations" json:"locations"`
	GroupsAndCreatures string   `yaml:"groups_and_creatures" json:"groups_and_creatures"`
	BadOutcomes        string   `yaml:"bad_outcomes" json:"bad_outcomes"`
	History            []string `yaml:"history" json:"history"`
	Ideals             []string `yaml:"ideals" json:"ideals"`
	Burdens            []string `yaml:"burdens" json:"burdens"`
}

type PipCard struct {
	ID             string `yaml:"id" json:"id"`
	Name           string `yaml:"name" json:"name"`
	Suit           string `yaml:"suit" json:"suit"`
	Rank           string `yaml:"rank" json:"rank"`
	ChallengeValue int    `yaml:"challenge_value" json:"challenge_value"`
	FateValue      int    `yaml:"fate_value" json:"fate_value"`
}

type Cards struct {
	Vision []VisionCard `yaml:"vision"`
	Pips   []PipCard    `yaml:"pips"`
}

type Skill struct {
	ID               string `yaml:"id" json:"id"`
	Name             string `yaml:"name" json:"name"`
	Suit             string `yaml:"suit" json:"suit"`
	SheetDescription string `yaml:"sheet_description" json:"sheet_description"`
}

type Skills struct {
	Skills []Skill `yaml:"skills"`
}

type Clock struct {
	Segments int    `yaml:"segments" json:"segments"`
	Name     string `yaml:"name" json:"name"`
}

type Ability struct {
	ID        string `yaml:"id" json:"id"`
	Name      string `yaml:"name" json:"name"`
	Text      string `yaml:"text" json:"text"`
	SheetText string `yaml:"sheet_text,omitempty" json:"sheet_text,omitempty"`
	Page      int    `yaml:"page" json:"page"`
	Clock     *Clock `yaml:"clock,omitempty" json:"clock,omitempty"`
}

type Item struct {
	Name                string `yaml:"name" json:"name"`
	SingleUse           bool   `yaml:"single_use" json:"single_use"`
	RulebookDescription bool   `yaml:"rulebook_description" json:"rulebook_description"`
	Description         string `yaml:"description,omitempty" json:"description,omitempty"`
}

type Class struct {
	ID                         string         `yaml:"id" json:"id"`
	Name                       string         `yaml:"name" json:"name"`
	Guild                      string         `yaml:"guild" json:"guild"`
	AbilityList                string         `yaml:"ability_list" json:"ability_list"`
	AbilityXPTrack             string         `yaml:"ability_xp_track" json:"ability_xp_track"`
	Motto                      string         `yaml:"motto" json:"motto"`
	Summary                    string         `yaml:"summary" json:"summary"`
	Pages                      string         `yaml:"pages" json:"pages"`
	PrefilledSkills            map[string]int `yaml:"prefilled_skills" json:"prefilled_skills"`
	StartsWithAdeptProficiency bool           `yaml:"starts_with_adept_proficiency" json:"starts_with_adept_proficiency"`
	Abilities                  []Ability      `yaml:"abilities" json:"abilities"`
	Items                      []Item         `yaml:"items" json:"items"`
}

type Classes struct {
	Precedence  string  `yaml:"precedence"`
	CommonItems []Item  `yaml:"common_items"`
	Classes     []Class `yaml:"classes"`
}

type Adventure struct {
	ID    string `yaml:"id" json:"id"`
	Title string `yaml:"title" json:"title"`
	Book  string `yaml:"book" json:"book"`
	File  string `yaml:"file" json:"-"` // Seer-only text
}

type Adventures struct {
	Adventures []Adventure `yaml:"adventures"`
}

// Campaign holds the enumerations from data/campaign.yaml.
type Campaign struct {
	AgentStatus     []string `yaml:"agent_status"`
	ContactKind     []string `yaml:"contact_kind"`
	SessionStatus   []string `yaml:"session_status"`
	AdversaryStatus []string `yaml:"adversary_status"`
	ClockScope      []string `yaml:"clock_scope"`
	ClockStatus     []string `yaml:"clock_status"`
	Territories     []string `yaml:"territories"`
	CampaignMode    []string `yaml:"campaign_mode"`
	Visibility      []string `yaml:"visibility"`
}

// Prompt is one plugin skill (SKILL.md), served as an MCP prompt.
type Prompt struct {
	Name         string `yaml:"name"`
	Description  string `yaml:"description"`
	ArgumentHint string `yaml:"argument-hint"`
	Body         string `yaml:"-"`
}

// Snapshot is one validated copy of the game data, read-only once loaded.
type Snapshot struct {
	ID       string // git commit (or "local-…" for a local path source)
	Dir      string // snapshot directory on disk
	LoadedAt time.Time

	Manifest   Manifest
	Cards      Cards
	Skills     Skills
	Classes    Classes
	Campaign   Campaign
	Adventures Adventures
	Limits     Limits
	Prompts    []Prompt
	Homebrew   Homebrew
	// Tables the app doesn't type yet (limits, setting, oracle, downtime), keyed by manifest name.
	Raw map[string]map[string]any
}

// Homebrew is supplementary data written for the table, NOT from the published books. It's
// optional (older rules releases have none), and anything built from it must say it's homebrew.
type Homebrew struct {
	Names *HomebrewNames
}

// HomebrewNames adds family names and bynames to the books' given names per city, and names for
// Dioscoria (the books print none).
type HomebrewNames struct {
	Source  string           `yaml:"source"`
	Note    string           `yaml:"note"`
	Regions []HomebrewRegion `yaml:"regions"`
}

type HomebrewRegion struct {
	Region     string   `yaml:"region" json:"region"`
	Convention string   `yaml:"convention" json:"convention"`
	Given      []string `yaml:"given" json:"given,omitempty"`
	Before     []string `yaml:"before" json:"before,omitempty"` // bynames before the given name
	After      []string `yaml:"after" json:"after,omitempty"`   // family names or bynames after it
}

// HomebrewRegion returns the homebrew names for a region, or nil.
func (s *Snapshot) HomebrewRegion(region string) *HomebrewRegion {
	if s.Homebrew.Names == nil {
		return nil
	}
	for i := range s.Homebrew.Names.Regions {
		if s.Homebrew.Names.Regions[i].Region == region {
			return &s.Homebrew.Names.Regions[i]
		}
	}
	return nil
}

// Class returns the class with the given id or name.
func (s *Snapshot) Class(idOrName string) *Class {
	for i := range s.Classes.Classes {
		c := &s.Classes.Classes[i]
		if c.ID == idOrName || c.Name == idOrName {
			return c
		}
	}
	return nil
}

// Label is how the snapshot is shown: the release ("v1.2.0"), or the manifest version plus the
// commit or "local" for a branch or a local directory.
func (s *Snapshot) Label() string {
	if v := semverOf(s.ID); v != "" {
		return v
	}
	v := "unversioned"
	if s.Manifest.Version != "" {
		v = "v" + s.Manifest.Version
	}
	if strings.HasPrefix(s.ID, "local-") {
		return v + " (local)"
	}
	return v + " (" + s.Short() + ")"
}

// Short returns the first 8 characters of the snapshot id.
func (s *Snapshot) Short() string {
	if len(s.ID) > 8 {
		return s.ID[:8]
	}
	return s.ID
}
