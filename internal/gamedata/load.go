package gamedata

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const manifestFile = "hidden-isle-data.yaml"

// Load reads and validates the snapshot in dir. Any problem is returned as one error listing all of them.
func Load(dir, id string) (*Snapshot, error) {
	s := &Snapshot{ID: id, Dir: dir, LoadedAt: time.Now(), Raw: map[string]map[string]any{}}
	if err := readYAML(filepath.Join(dir, manifestFile), &s.Manifest); err != nil {
		return nil, err
	}
	if !slices.Contains(SupportedSchemas, s.Manifest.SchemaVersion) {
		return nil, fmt.Errorf("manifest schema_version %d is not supported by this app (supports %v); update the app", s.Manifest.SchemaVersion, SupportedSchemas)
	}
	typed := map[string]any{"cards": &s.Cards, "skills": &s.Skills, "classes": &s.Classes, "campaign": &s.Campaign, "adventures": &s.Adventures, "limits": &s.Limits}
	var errs []error
	for key, rel := range s.Manifest.files() {
		path, err := within(dir, rel)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if dst, ok := typed[key]; ok {
			errs = append(errs, readYAML(path, dst))
			continue
		}
		m := map[string]any{}
		errs = append(errs, readYAML(path, &m))
		s.Raw[key] = m
	}
	for _, key := range []string{"cards", "skills", "classes", "campaign", "adventures", "limits"} {
		if _, ok := s.Manifest.files()[key]; !ok {
			errs = append(errs, fmt.Errorf("manifest: data file %q is missing", key))
		}
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	if err := s.loadPrompts(); err != nil {
		return nil, err
	}
	if err := s.validate(); err != nil {
		return nil, err
	}
	return s, nil
}

func (m *Manifest) files() map[string]string {
	out := map[string]string{}
	for k, v := range m.Data.Generated {
		out[k] = v
	}
	for k, v := range m.Data.Hand {
		out[k] = v
	}
	return out
}

// paths lists every repo-relative path a snapshot must contain.
func (m *Manifest) paths() []string {
	out := []string{manifestFile}
	for _, v := range m.files() {
		out = append(out, v)
	}
	for _, t := range m.Text {
		out = append(out, t.Path)
	}
	if m.Prompts != "" {
		out = append(out, m.Prompts)
	}
	return out
}

// loadPrompts reads <dir>/<prompts>/*/SKILL.md: YAML front matter, then the skill's text.
func (s *Snapshot) loadPrompts() error {
	if s.Manifest.Prompts == "" {
		return nil
	}
	root, err := within(s.Dir, s.Manifest.Prompts)
	if err != nil {
		return err
	}
	files, err := filepath.Glob(filepath.Join(root, "*", "SKILL.md"))
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			return err
		}
		text := string(b)
		if !strings.HasPrefix(text, "---") {
			return fmt.Errorf("%s: no front matter", f)
		}
		parts := strings.SplitN(text[3:], "\n---", 2)
		if len(parts) != 2 {
			return fmt.Errorf("%s: unterminated front matter", f)
		}
		var p Prompt
		if err := yaml.Unmarshal([]byte(parts[0]), &p); err != nil {
			return fmt.Errorf("%s: %w", f, err)
		}
		if p.Name == "" || seen[p.Name] {
			return fmt.Errorf("%s: missing or duplicate name %q", f, p.Name)
		}
		seen[p.Name] = true
		p.Body = strings.TrimSpace(strings.TrimPrefix(parts[1], "\n"))
		s.Prompts = append(s.Prompts, p)
	}
	return nil
}

func (s *Snapshot) validate() error {
	var errs []error
	check := func(ok bool, format string, args ...any) {
		if !ok {
			errs = append(errs, fmt.Errorf(format, args...))
		}
	}
	check(len(s.Cards.Vision) == 38, "cards: expected 38 vision cards, got %d", len(s.Cards.Vision))
	check(len(s.Cards.Pips) == 40, "cards: expected 40 pips, got %d", len(s.Cards.Pips))
	check(len(s.Skills.Skills) == 12, "skills: expected 12, got %d", len(s.Skills.Skills))
	check(len(s.Classes.Classes) >= 8, "classes: expected at least 8, got %d", len(s.Classes.Classes))
	skills := map[string]bool{}
	for _, sk := range s.Skills.Skills {
		skills[sk.Name] = true
	}
	for _, c := range s.Classes.Classes {
		check(len(c.Abilities) > 0, "classes: %s has no abilities", c.Name)
		for sk := range c.PrefilledSkills {
			check(skills[sk], "classes: %s pre-fills unknown skill %q", c.Name, sk)
		}
		for _, a := range c.Abilities {
			check(a.Text != "" && a.Page > 0, "classes: %s/%s needs text and page", c.Name, a.Name)
		}
	}
	check(len(s.Campaign.Territories) > 0, "campaign: no territories")
	l := s.Limits
	check(l.Agent.Skill.Max > 0 && l.Agent.Skill.MaxUnlocked >= l.Agent.Skill.Max, "limits: agent.skill missing")
	check(l.Agent.HarmPerSuit.Max > 0 && len(l.Agent.HarmPerSuit.Types) > 0, "limits: agent.harm_per_suit missing")
	check(l.Agent.BurdenTrack.Max > 0 && l.Agent.SuitXP.Max > 0 && l.Agent.LoadUsed.Max > 0, "limits: agent tracks missing")
	check(l.Contact.Affection.Max > 0 && l.Contact.Distance.Max > 0, "limits: contact missing")
	check(len(l.Clock.Segments) > 0, "limits: clock.segments missing")
	for _, t := range s.Manifest.Text {
		check(t.Audience == "party" || t.Audience == "seer", "manifest: text %s has bad audience %q", t.Path, t.Audience)
		_, err := os.Stat(filepath.Join(s.Dir, t.Path))
		check(err == nil, "manifest: text path %s missing", t.Path)
	}
	for _, a := range s.Adventures.Adventures {
		check(s.Audience(a.File) == "seer", "adventures: %s must be in a seer-only text source", a.File)
		_, err := os.Stat(filepath.Join(s.Dir, a.File))
		check(err == nil, "adventures: %s missing", a.File)
	}
	return errors.Join(errs...)
}

// Audience returns who may read the repo-relative path rel: "party", "seer", or "" if it isn't
// in any text source. The most specific (longest) matching source wins.
func (s *Snapshot) Audience(rel string) string {
	best, aud := -1, ""
	for _, t := range s.Manifest.Text {
		if rel == t.Path || strings.HasPrefix(rel, strings.TrimSuffix(t.Path, "/")+"/") {
			if len(t.Path) > best {
				best, aud = len(t.Path), t.Audience
			}
		}
	}
	return aud
}

func readYAML(path string, dst any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := yaml.Unmarshal(b, dst); err != nil {
		return fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	return nil
}

// within joins rel onto dir and refuses paths that escape it.
func within(dir, rel string) (string, error) {
	if filepath.IsAbs(rel) || !filepath.IsLocal(filepath.FromSlash(rel)) {
		return "", fmt.Errorf("manifest: path %q must be relative and inside the repo", rel)
	}
	return filepath.Join(dir, filepath.FromSlash(rel)), nil
}
