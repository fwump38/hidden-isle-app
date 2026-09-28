package assist

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"

	"github.com/fwump38/hidden-isle-app/internal/db"
)

// Suggest is the creation wizard's background helper: one structured call that returns a few
// options for a single step, with no thread, no stored messages and no tools beyond the one
// forced output tool. The player never sees a chat; the wizard shows the options as choice
// buttons, and nothing is saved until the player picks one and saves the step themselves.
// It spends from the same monthly budgets as the chat.

// SuggestField is one value each option carries, e.g. a burden's word or a contact's name.
type SuggestField struct {
	Name string
	Desc string
}

// suggestKind is what the model is asked for at one step.
type suggestKind struct {
	Ask      string
	Fields   []SuggestField
	Count    int
	Audience string // "seer" for the Seer's own suggestion boxes; "" (player) is the default
}

var suggestKinds = map[string]suggestKind{
	"class": {Ask: "Suggest which Hidden Isle classes fit the character the player describes.", Count: 2,
		Fields: []SuggestField{{"class", "Exactly one class name from the list given"}}},
	"name": {Ask: "Suggest full names for this Agent, fitting the year 1562 and their culture or homeland if one is given.", Count: 4,
		Fields: []SuggestField{{"name", "The full name only"}}},
	"child": {Ask: `Suggest ways to complete "As a child, I solved problems by…", in the spirit of the drawn card's meaning and phrases.`, Count: 3,
		Fields: []SuggestField{{"phrase", "A short -ing phrase of 2-7 words, without the leading 'by'"}}},
	"adult": {Ask: `Suggest ways to complete "As an adult, I survived (or flourished) by…", in the spirit of the drawn card's meaning and phrases.`, Count: 3,
		Fields: []SuggestField{{"verb", `"survived" or "flourished"`}, {"phrase", "A short -ing phrase of 2-7 words, without the leading 'by'"}}},
	"burden": {Ask: "Suggest a Burden: a flaw that drives the Agent into trouble, fitting the drawn card and their core self.", Count: 4,
		Fields: []SuggestField{{"word", "One adjective or -ing verb, capitalised (p. 40)"}}},
	"ideal": {Ask: "Suggest an Ideal: what the Agent aspires to, fitting the drawn card and their core self.", Count: 4,
		Fields: []SuggestField{{"word", "One adjective or -ing verb, capitalised (p. 40)"}}},
	"abilities": {Ask: "Suggest which of the class's abilities best fit this Agent's core self and concept.", Count: 3,
		Fields: []SuggestField{{"ability", "Exactly one ability name from the class list given"}}},
	// The exact split isn't hardcoded here (it isn't always 2 core-self + 2 free — some may
	// already be spent, or the campaign's own limits differ) since a fixed number here would
	// contradict "Points to add: N" in the details and reliably shorted the total.
	"skills": {Ask: "Suggest where to add this Agent's remaining skill points: some inspired by their core self, the rest anywhere, within the limits given.", Count: 2,
		Fields: []SuggestField{{"points", `Exactly the form "Skill +N, Skill +N, …", using only skill names from the list given. Must add up to exactly the "points to add" number in the details — use all of it, never more, and never push a skill past the "max in any skill" limit also given there`}}},
	"magic": {Ask: "Suggest a school of magic for this Agent's Adept proficiency that fits their concept.", Count: 2,
		Fields: []SuggestField{{"school", "Exactly one school name from the list given"}}},
	"look": {Ask: "Suggest how this Agent appears: an age, a culture and a short look that echoes their drawn cards.", Count: 3,
		Fields: []SuggestField{{"age", "An age, as a number or short phrase"}, {"culture", "A culture, e.g. Venetian"}, {"look", "One or two sentences"}}},
	// The field is "sentence", not "why": naming it "why" (a near-synonym of the "reason" property
	// every kind carries, "why this suggestion fits") reliably got the model to swap the two —
	// the in-fiction answer landing in "reason" and the meta-justification landing in "why".
	"why": {Ask: "Suggest why this Agent came to Dioscoria, tailored to their core self and culture.", Count: 3,
		Fields: []SuggestField{{"sentence", "One sentence: why they came to Dioscoria. This is the answer itself, not a justification for the suggestion — that's \"reason\""}}},
	"contact": {Ask: "Suggest who this contact is, reading the drawn card loosely as their personality.", Count: 3,
		Fields: []SuggestField{{"name", "A full name fitting their land"}, {"land", "Their land (or Dioscorian district)"}, {"description", "Profession and personality, one or two sentences"}}},
	"ability_contact": {Ask: "Suggest who or what this granted contact is: a being or patron an ability grants (a deity, angel or demon), not an ordinary contact. Fit the Agent's core self.", Count: 3,
		Fields: []SuggestField{{"name", "A name or title fitting them"}, {"description", "One or two sentences: who or what they are"}}},

	// The Seer's own suggestion boxes.
	"adversary": {Audience: "seer", Count: 2,
		Ask: "Suggest a new adversary that fits the campaign so far: a group, cult, family or organisation with a plot the Hand could uncover.",
		Fields: []SuggestField{{"name", "A short, evocative name"}, {"leader", "The leader's name (invent one if the group doesn't obviously have one yet)"},
			{"plot", "One sentence: what they're doing"}, {"motivation", "One sentence: why"},
			{"members", "Up to 4 members, each with a short motivation, one line"}}},
	"session": {Audience: "seer", Count: 2,
		Ask:    "Suggest a session title and prep (cast, locations, clocks, likely challenges, twists) that follows on from the campaign so far.",
		Fields: []SuggestField{{"title", "A short session title"}, {"prep", "Cast, locations, clocks, likely challenges, twists — a short paragraph"}}},
	"clock": {Audience: "seer", Count: 3,
		Ask: "Suggest a clock: what happens when it fills (p. 86), fitting the campaign so far.",
		Fields: []SuggestField{{"name", "What happens when it fills, one line"}, {"segments", "One of the segment counts given"},
			{"linked_to", "What it's tied to (a territory, adversary, ritual…), or empty"}}},
	"territory_event": {Audience: "seer", Count: 3,
		Ask:    "Suggest a territory event: something that's changed or is brewing there, fitting the campaign so far (p. 82, \"the world changes\").",
		Fields: []SuggestField{{"event", "One or two sentences"}}},
	"handout": {Audience: "seer", Count: 2,
		Ask:    "Suggest a handout: a short title and the text the players will read aloud or see verbatim, fitting the campaign so far.",
		Fields: []SuggestField{{"title", "A short title"}, {"body", "The text itself, one or two sentences"}}},
}

// SuggestFields lists the fields an option of kind carries, or nil for an unknown kind.
func SuggestFields(kind string) []SuggestField { return suggestKinds[kind].Fields }

// SuggestRequest is one step's request. Context is plain lines describing the Agent so far
// (class, drawn cards, chosen phrases…); the caller builds it from the rules data. Brief is
// campaign context for the Seer's own suggestion boxes (nil for the wizard's).
type SuggestRequest struct {
	Kind    string
	Context []string
	Brief   *Brief
	Hint    string   // what the player or Seer describes they're after
	Exclude []string // options already on screen, so "more" gives new ones
	// Enum constrains a field to exactly one of a fixed set of values (keyed by SuggestField.Name),
	// e.g. the real class names, so the model can't return something that reads as plausible but
	// matches nothing in wizChoiceFor and silently vanishes ("nothing usable came back").
	Enum map[string][]string
	// Count overrides suggestKind's own Count when set, e.g. the wizard asking for exactly one
	// contact per card it already drew, instead of that kind's usual few-per-call default.
	Count int
}

// Suggestion is one option: its field values and a short reason.
type Suggestion struct {
	Fields map[string]string
	Why    string
}

const suggestPlayerInstructions = `You help a player create a character for The Hidden Isle, a tarot RPG of sorcery and adventure set in 1562, inside the campaign app. You only ever answer through the offer_suggestions tool.

- Offer distinct, evocative options that fit 1562 and the details given. Keep them short.
- Use only names, classes, abilities, skills and schools exactly as listed in the details; never invent rules or numbers.
- Never repeat an option listed under "Already shown".
- If the player describes what they're thinking, follow it closely.
- "why" is one short line saying why the option fits.`

const suggestSeerInstructions = `You help the Seer (GM) run The Hidden Isle, a tarot RPG of sorcery and adventure set in 1562, inside the campaign app. You only ever answer through the offer_suggestions tool.

- Offer distinct, evocative options that fit 1562 and the campaign details given. Keep them short.
- Use only names and facts exactly as listed in the campaign details; never invent a rule or a number, and never contradict something already established there.
- Never repeat an option listed under "Already shown".
- If the Seer describes what they're after, follow it closely.
- "why" is one short line saying why the option fits.`

const suggestTokens = 800

// ErrSuggestUnavailable is what a player sees when the API call itself fails.
var ErrSuggestUnavailable = errors.New("more ideas aren't available right now; pick one of the options above or write your own")

// Suggest asks for options for one creation step.
func (s *Service) Suggest(ctx context.Context, u *db.User, req SuggestRequest) ([]Suggestion, error) {
	kind, ok := suggestKinds[req.Kind]
	if !ok {
		return nil, fmt.Errorf("nothing to suggest for %q", req.Kind)
	}
	if err := s.checkRateIn(s.recentSuggest, u.ID, suggestRateCount); err != nil {
		return nil, err
	}
	month := currentMonth()
	userUSD, globalUSD := s.monthSpend(month, u.ID)
	if s.cfg.PlayerCapUSD > 0 && userUSD >= s.cfg.PlayerCapUSD {
		return nil, fmt.Errorf("%w (your monthly cap)", ErrBudget)
	}
	if s.cfg.GlobalCapUSD > 0 && globalUSD >= s.cfg.GlobalCapUSD {
		return nil, fmt.Errorf("%w (the table's monthly cap)", ErrBudget)
	}

	count := kind.Count
	if req.Count > 0 {
		count = req.Count
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s Offer %d options.\n", kind.Ask, count)
	if len(req.Context) > 0 {
		b.WriteString("\nDetails:\n")
		for _, l := range req.Context {
			fmt.Fprintf(&b, "- %s\n", l)
		}
	}
	if lines := req.Brief.Lines(); len(lines) > 0 {
		b.WriteString("\nCampaign details:\n")
		for _, l := range lines {
			fmt.Fprintf(&b, "- %s\n", l)
		}
	}
	who := "the player is"
	if kind.Audience == "seer" {
		who = "the Seer is"
	}
	if h := strings.TrimSpace(req.Hint); h != "" {
		fmt.Fprintf(&b, "\nWhat %s thinking: %s\n", who, h)
	}
	if len(req.Exclude) > 0 {
		fmt.Fprintf(&b, "\nAlready shown: %s\n", strings.Join(req.Exclude, "; "))
	}

	// The rationale property is named "reason" rather than "why" so it can't collide with a
	// field the kind itself defines (the "why" kind's own field is named "why" — p. 41's "why
	// Dioscoria" — which previously duplicated this key and produced an invalid JSON schema:
	// draft 2020-12 requires "required" to list unique names).
	props := map[string]any{"reason": strProp("One short line: why this fits")}
	required := []string{"reason"}
	for _, f := range kind.Fields {
		p := strProp(f.Desc)
		if enum := req.Enum[f.Name]; len(enum) > 0 {
			p["enum"] = enum
		}
		props[f.Name] = p
		required = append(required, f.Name)
	}
	tool := newTool("offer_suggestions", "Offer the options to the player.", map[string]any{
		"options": map[string]any{"type": "array", "items": map[string]any{
			"type": "object", "properties": props, "required": required,
		}},
	}, "options")

	instructions := suggestPlayerInstructions
	if kind.Audience == "seer" {
		instructions = suggestSeerInstructions
	}
	resp, err := s.client.Messages.New(ctx, anthropic.MessageNewParams{
		Model: anthropic.Model(s.cfg.Model), MaxTokens: suggestTokens,
		System:     []anthropic.TextBlockParam{{Text: instructions, CacheControl: anthropic.NewCacheControlEphemeralParam()}},
		Messages:   []anthropic.MessageParam{anthropic.NewUserMessage(anthropic.NewTextBlock(b.String()))},
		Tools:      []anthropic.ToolUnionParam{tool},
		ToolChoice: anthropic.ToolChoiceParamOfTool("offer_suggestions"),
	})
	if err != nil {
		// The API's own error (bad key, overloaded…) means nothing to a player; the Seer finds it in the log.
		slog.Warn("creation suggestions", "kind", req.Kind, "err", err)
		return nil, ErrSuggestUnavailable
	}
	s.recordUsage(month, u.ID, resp.Usage.InputTokens+resp.Usage.CacheCreationInputTokens+resp.Usage.CacheReadInputTokens, resp.Usage.OutputTokens)

	for _, blk := range resp.Content {
		if blk.Type != "tool_use" || blk.Name != "offer_suggestions" {
			continue
		}
		var in struct {
			Options []map[string]any `json:"options"`
		}
		if err := json.Unmarshal(blk.Input, &in); err != nil {
			return nil, fmt.Errorf("couldn't read the suggestions: %w", err)
		}
		var out []Suggestion
		for _, o := range in.Options {
			sg := Suggestion{Fields: map[string]string{}}
			if w, ok := o["reason"].(string); ok {
				sg.Why = strings.TrimSpace(w)
			}
			for _, f := range kind.Fields {
				if v, ok := o[f.Name].(string); ok && strings.TrimSpace(v) != "" {
					sg.Fields[f.Name] = strings.TrimSpace(v)
				}
			}
			if len(sg.Fields) == len(kind.Fields) {
				out = append(out, sg)
			}
		}
		if len(out) > 0 {
			return out, nil
		}
	}
	return nil, errors.New("no suggestions came back; try again or describe what you're after")
}
