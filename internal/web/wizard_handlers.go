package web

import (
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/fwump38/hidden-isle-app/internal/assist"
	"github.com/fwump38/hidden-isle-app/internal/campaign"
	"github.com/fwump38/hidden-isle-app/internal/cards"
	"github.com/fwump38/hidden-isle-app/internal/creation"
	"github.com/fwump38/hidden-isle-app/internal/db"
	"github.com/fwump38/hidden-isle-app/internal/gamedata"
)

// The creation wizard walks a player through pp. 40-42 (Ref p. 7) one screen at a time, after
// they've picked a class. It's a thin layer over the sheet's own endpoints: every field it saves
// goes through the same POST /agents/{id} and /agents/{id}/contacts as the sheet, so it gets the
// same permission checks, validation and change log for free.
//
// Each screen offers choices the player can pick with one tap: first the book's own (a drawn
// card's phrases, burdens or ideals; the class's abilities; the schools of magic; the regional
// name lists), then, when the in-app assistant is configured, more ideas from Claude in the
// background (wizardSuggest). Claude never writes to the sheet here and there's no chat to read:
// picking an option only fills in the form, and the player saves it themselves.
//
// There's no stored "current step": it's inferred from what's already filled in, so leaving
// and coming back (or a Seer helping mid-way) always lands somewhere sensible. Every step is
// reachable from the stepper, and each has a Back button.

func (s *Server) registerWizard(mux *http.ServeMux) {
	u := func(h http.HandlerFunc) http.Handler { return s.requireUser(h) }
	mux.Handle("GET /agents/{id}/wizard", u(s.wizardPage))
	mux.Handle("GET /agents/{id}/wizard/card", u(s.wizardCard))
	mux.Handle("POST /agents/{id}/wizard/draw", u(s.wizardDraw))
	mux.Handle("GET /agents/{id}/wizard/names", u(s.wizardNames))
	mux.Handle("POST /agents/{id}/wizard/suggest", u(s.wizardSuggest))
}

type wizStepInfo struct {
	N     int
	Key   string
	Title string
}

var wizStepList = []wizStepInfo{
	{1, "name", "Name"}, {2, "child", "Childhood"}, {3, "adult", "Adulthood"}, {4, "burden", "Burden"},
	{5, "ideal", "Ideal"}, {6, "abilities", "Abilities"}, {7, "skills", "Skills"}, {8, "magic", "Magic"},
	{9, "look", "Look"}, {10, "why", "Why Dioscoria"}, {11, "homeland", "Homeland contact"},
	{12, "dioscorian", "Dioscorian contact"}, {13, "done", "Done"},
}

// wizLastStep is the review screen at the end.
const wizLastStep = 13

// wizStepNum reads ?step= as a number or a key ("burden"); 0 if it's neither.
func wizStepNum(v string) int {
	if n, err := strconv.Atoi(v); err == nil && n >= 1 && n <= wizLastStep {
		return n
	}
	for _, st := range wizStepList {
		if st.Key == v {
			return st.N
		}
	}
	return 0
}

// unnamed is the placeholder name an Agent gets when its class is picked; the wizard's first
// screen replaces it.
const unnamed = "New Agent"

// wizardData is what the wizard template renders.
type wizardData struct {
	Agent         *db.Agent
	Class         *gamedata.Class
	Solo          bool
	Limits        *gamedata.Limits
	Step          int
	Key           string
	Steps         []wizStepInfo
	Done          map[int]bool
	Vision        []string
	Homeland      *db.Contact
	Dioscorian    *db.Contact
	Chosen        map[string]bool // ability id -> already on the sheet
	Abil          []wizardAbility // the Agent's chosen abilities, resolved to their name and text
	AssistEnabled bool            // Claude's background suggestions are available
	Regions       []string        // where names can come from
	Schools       []creation.School
	WhyReasons    []string
	Skills        map[string][]string // suit -> skill names
	SuitHelp      map[string]string   // suit -> the same explanation the full sheet shows (p. 14, p. 23)
	Cards         map[string]wizCard  // the card pickers on this screen, by step key
	Ready         bool                // every step is complete, independent of any ?step= override
	MissingSteps  []wizStepInfo       // steps still incomplete, for the Done screen
	Error         string
}

// URL is the wizard URL for a step key.
func (d wizardData) URL(key string) string {
	return fmt.Sprintf("/agents/%d/wizard?step=%s", d.Agent.ID, key)
}

// Prev is where the Back button goes: the step before, or on the first screen the class page
// (to change the class the Agent was created with).
func (d wizardData) Prev() string {
	if d.Step <= 1 {
		return fmt.Sprintf("/agents/create/class?agent=%d&campaign_id=%d", d.Agent.ID, d.Agent.CampaignID)
	}
	prev := d.Step - 1
	if prev == 8 && !d.NeedsMagic() {
		prev = 7
	}
	return d.URL(wizStepList[prev-1].Key)
}

// Next is the step after this one (skipping magic for a class that doesn't choose it).
func (d wizardData) Next() string {
	next := d.Step + 1
	if next == 8 && !d.NeedsMagic() {
		next = 9
	}
	if next > wizLastStep {
		next = wizLastStep
	}
	return d.URL(wizStepList[next-1].Key)
}

// NeedsMagic says whether the class starts with an Adept proficiency (p. 41).
func (d wizardData) NeedsMagic() bool {
	return d.Class != nil && d.Class.StartsWithAdeptProficiency
}

// Card returns the card picker for a step key (the template can't index a map of structs
// and call methods in one go).
func (d wizardData) Card(key string) wizCard { return d.Cards[key] }

// wizardAbility is one chosen ability with its text resolved from the class data: the sheet
// stores only the ability's id (rules text can be corrected later without touching every Agent).
type wizardAbility struct {
	Index        int
	Name         string
	Text         string
	NeedsContact bool // grants a contact (The Old Ways, Celestial Bargain) that isn't named yet
}

func resolveAbilities(abilities []db.AgentAbility, class *gamedata.Class) []wizardAbility {
	var out []wizardAbility
	for i, ab := range abilities {
		v := wizardAbility{Index: i, Name: ab.Name, Text: ab.Text,
			NeedsContact: campaign.AbilityGrantsContact(ab.ID) && ab.GrantedContactID == nil}
		if ab.ID != "" && class != nil {
			for _, x := range class.Abilities {
				if x.ID == ab.ID {
					v.Name, v.Text = x.Name, x.Text
				}
			}
		}
		out = append(out, v)
	}
	return out
}

// stepDone says whether step n (1-12; 13, the review screen, is handled separately as "Ready")
// is complete on its own terms — the single source of truth for both creationStep (which step to
// land on) and the wizard's per-step checkmarks, so a checkmark can't disagree with where the
// wizard would actually send the player.
func stepDone(n int, ag *db.Agent, class *gamedata.Class, l *gamedata.Limits, solo bool, contacts []db.Contact) bool {
	switch n {
	case 1:
		return strings.TrimSpace(ag.Name) != "" && ag.Name != unnamed
	case 2:
		return strings.TrimSpace(ag.ChildPhrase) != ""
	case 3:
		return strings.TrimSpace(ag.AdultPhrase) != "" && ag.AdultVerb != ""
	case 4:
		return strings.TrimSpace(ag.Burden) != ""
	case 5:
		return strings.TrimSpace(ag.Ideal) != ""
	case 6:
		return len(ag.Abilities) >= l.Creation.Abilities
	case 7:
		return skillTotal(ag) >= creationTotalPoints(l, solo)
	case 8:
		if class == nil || !class.StartsWithAdeptProficiency {
			return true // not a magical class: nothing to choose, never blocks
		}
		return len(ag.Proficiencies) > 0
	case 9:
		return strings.TrimSpace(ag.Look) != ""
	case 10:
		return strings.TrimSpace(ag.Why) != ""
	case 11:
		return hasContactKind(contacts, "Homeland")
	case 12:
		return hasContactKind(contacts, "Dioscorian")
	}
	return true
}

// creationStep works out the first incomplete step for ag (1-13; 13 = everything's there).
func creationStep(ag *db.Agent, class *gamedata.Class, l *gamedata.Limits, solo bool, contacts []db.Contact) int {
	for _, st := range wizStepList {
		if st.N == wizLastStep {
			break
		}
		if !stepDone(st.N, ag, class, l, solo, contacts) {
			return st.N
		}
	}
	return wizLastStep
}

func skillTotal(ag *db.Agent) int {
	n := 0
	for _, v := range ag.Skills {
		n += v
	}
	return n
}

func creationTotalPoints(l *gamedata.Limits, solo bool) int {
	if solo && l.Creation.Solo.TotalPoints > 0 {
		return l.Creation.Solo.TotalPoints
	}
	return l.Creation.TotalPoints
}

func creationMaxSkill(l *gamedata.Limits, solo bool) int {
	if solo && l.Creation.Solo.MaxSkill > 0 {
		return l.Creation.Solo.MaxSkill
	}
	return l.Creation.MaxSkill
}

func hasContactKind(contacts []db.Contact, kind string) bool {
	for _, c := range contacts {
		if c.Kind == kind {
			return true
		}
	}
	return false
}

// wizardAgent loads the Agent a wizard request is about, checking the caller may edit it.
func (s *Server) wizardAgent(r *http.Request) (campaign.Actor, *db.Agent, *gamedata.Snapshot, error) {
	a := s.actor(r)
	ag, err := s.Svc.Agent(a, pathID(r, "id"))
	if err != nil {
		return a, nil, nil, err
	}
	if !s.Svc.CanEditAgent(a, ag) {
		return a, nil, nil, campaign.ErrForbidden
	}
	snap := s.Data.Current()
	if snap == nil {
		return a, nil, nil, campaign.ErrNoData
	}
	return a, ag, snap, nil
}

func (s *Server) wizardPage(w http.ResponseWriter, r *http.Request) {
	a, ag, snap, err := s.wizardAgent(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	d := s.buildWizardData(a, ag, snap)
	if n := wizStepNum(r.URL.Query().Get("step")); n != 0 {
		d.setStep(n)
	}
	var c *db.Campaign
	if ag.CampaignID != 0 {
		c, _ = s.Svc.Campaign(a, ag.CampaignID)
	}
	title := "Create an Agent"
	if ag.Name != unnamed {
		title = "Create " + ag.Name
	}
	s.render(w, r, "wizard", http.StatusOK, pageData{Title: title, Error: takeFlash(w, r), Data: d,
		Nav: &campaignNav{Campaign: c, Section: "agents", Snap: snap}})
}

func (d *wizardData) setStep(n int) {
	d.Step = n
	d.Key = wizStepList[n-1].Key
}

// isSolo says whether ag belongs to a solitaire-mode campaign (p. 96's looser skill cap).
func (s *Server) isSolo(a campaign.Actor, ag *db.Agent) bool {
	if ag.CampaignID == 0 {
		return false
	}
	c, err := s.Svc.Campaign(a, ag.CampaignID)
	return err == nil && c.Mode == "solitaire"
}

func (s *Server) buildWizardData(a campaign.Actor, ag *db.Agent, snap *gamedata.Snapshot) wizardData {
	class := snap.Class(ag.Class)
	solo := s.isSolo(a, ag)
	contacts, _ := s.Svc.Contacts(a, ag.ID)
	chosen := map[string]bool{}
	for _, ab := range ag.Abilities {
		if ab.ID != "" {
			chosen[ab.ID] = true
		}
	}
	d := wizardData{Agent: ag, Class: class, Solo: solo, Limits: &snap.Limits, Steps: wizStepList,
		Done: map[int]bool{}, Homeland: findContact(contacts, "Homeland"), Dioscorian: findContact(contacts, "Dioscorian"),
		Chosen: chosen, Abil: resolveAbilities(ag.Abilities, class), AssistEnabled: s.Assist != nil,
		Regions: creation.NameRegions(snap), Schools: creation.MagicSchools(snap), WhyReasons: creation.WhyReasons,
		Skills: map[string][]string{}, SuitHelp: suitHelp}
	current := creationStep(ag, class, &snap.Limits, solo, contacts)
	for _, st := range wizStepList {
		if st.N == wizLastStep {
			d.Done[st.N] = current == wizLastStep
			continue
		}
		d.Done[st.N] = stepDone(st.N, ag, class, &snap.Limits, solo, contacts)
		if !d.Done[st.N] {
			d.MissingSteps = append(d.MissingSteps, st)
		}
	}
	d.Ready = current == wizLastStep
	d.setStep(current)
	for _, v := range snap.Cards.Vision {
		d.Vision = append(d.Vision, v.Name)
	}
	for _, sk := range snap.Skills.Skills {
		d.Skills[sk.Suit] = append(d.Skills[sk.Suit], sk.Name)
	}
	d.Cards = map[string]wizCard{}
	for _, key := range []string{"child", "adult", "burden", "ideal", "homeland", "dioscorian"} {
		d.Cards[key] = newWizCard(snap, ag.ID, key, wizCardSaved(ag, key), nil)
	}
	return d
}

func findContact(contacts []db.Contact, kind string) *db.Contact {
	for i := range contacts {
		if contacts[i].Kind == kind {
			return &contacts[i]
		}
	}
	return nil
}

// ---------------------------------------------------------------- card pickers

// wizCard is one step's card picker: the card the player drew (typed in, or drawn digitally)
// and the options the book gives for it.
type wizCard struct {
	AgentID uint
	For     string // step key
	Input   string // the form field holding the card name
	Value   string // the card as entered
	Card    *gamedata.VisionCard
	Drawn   []*gamedata.VisionCard // a digital draw of several cards to pick from (contacts)
	Choices []wizChoice            // the book's options for the card
	Unknown bool                   // Value isn't a vision card
	// OOB renders this card picker as an htmx out-of-band swap into its existing #card-<For>
	// element, for when it's included alongside something else's response (wizardSuggest, when
	// it drew a fresh card the player hasn't seen: the suggestions alone would talk about "the
	// drawn card" without ever showing which one).
	OOB bool
}

// wizChoice is one option a player can pick: Fill maps form field names to the values picking
// it puts in the form.
type wizChoice struct {
	Label     string
	Why       string
	Fill      map[string]string
	AbilityID string // an ability suggestion: picking it adds the ability
	ClassID   string // a class suggestion: picking it chooses the class
}

// wizCardInput is the form field each card picker fills.
var wizCardInput = map[string]string{
	"child": "set.child_card", "adult": "set.adult_card", "burden": "set.burden_card", "ideal": "set.ideal_card",
	"homeland": "set.card", "dioscorian": "set.card",
	// "contact" isn't a card-picker step key (For never equals it); it's the suggestion Kind both
	// homeland and dioscorian's suggest-box shares, so a card wizardContext draws for either one
	// needs this entry to find its way back into the same "set.card" field they both already use.
	"contact": "set.card",
}

// wizCardForKey is the wizard-card picker's "For" (and so its #card-<For> element) for a
// suggestion Kind, or "" for a kind with no card picker at all. Every card-driven kind but
// "contact" already matches its own "For" one-for-one; "contact" is homeland's and dioscorian's
// shared suggestion Kind, so it needs the same set.kind lookup wizardContext's "contact" case uses.
func wizCardForKey(kind string, form url.Values) string {
	switch kind {
	case "child", "adult", "burden", "ideal":
		return kind
	case "contact":
		if form.Get("set.kind") == "Dioscorian" {
			return "dioscorian"
		}
		return "homeland"
	}
	return ""
}

func wizCardSaved(ag *db.Agent, key string) string {
	switch key {
	case "child":
		return ag.ChildCard
	case "adult":
		return ag.AdultCard
	case "burden":
		return ag.BurdenCard
	case "ideal":
		return ag.IdealCard
	}
	return ""
}

func findVision(snap *gamedata.Snapshot, name string) *gamedata.VisionCard {
	name = strings.TrimSpace(name)
	for i := range snap.Cards.Vision {
		v := &snap.Cards.Vision[i]
		if strings.EqualFold(v.Name, name) || strings.EqualFold(v.ID, name) {
			return v
		}
	}
	return nil
}

func newWizCard(snap *gamedata.Snapshot, agentID uint, key, value string, drawn []string) wizCard {
	c := wizCard{AgentID: agentID, For: key, Input: wizCardInput[key], Value: strings.TrimSpace(value)}
	for _, n := range drawn {
		if v := findVision(snap, n); v != nil {
			c.Drawn = append(c.Drawn, v)
		}
	}
	if c.Value == "" {
		return c
	}
	c.Card = findVision(snap, c.Value)
	if c.Card == nil {
		c.Unknown = true
		return c
	}
	c.Value = c.Card.Name
	switch key {
	case "child":
		for _, p := range c.Card.History {
			c.Choices = append(c.Choices, wizChoice{Label: p, Fill: map[string]string{"set.child_phrase": p}})
		}
	case "adult":
		for _, p := range c.Card.History {
			c.Choices = append(c.Choices, wizChoice{Label: p, Fill: map[string]string{"set.adult_phrase": p}})
		}
	case "burden":
		for _, p := range c.Card.Burdens {
			c.Choices = append(c.Choices, wizChoice{Label: p, Fill: map[string]string{"set.burden": p}})
		}
	case "ideal":
		for _, p := range c.Card.Ideals {
			c.Choices = append(c.Choices, wizChoice{Label: p, Fill: map[string]string{"set.ideal": p}})
		}
	}
	return c
}

// wizardCard re-renders a card picker for the card the player typed in.
func (s *Server) wizardCard(w http.ResponseWriter, r *http.Request) {
	_, ag, snap, err := s.wizardAgent(r)
	if err != nil {
		http.Error(w, friendly(err), http.StatusForbidden)
		return
	}
	key := r.FormValue("for")
	if _, ok := wizCardInput[key]; !ok {
		http.Error(w, "unknown step", http.StatusBadRequest)
		return
	}
	s.partial(w, "wizard", "wizard-card", newWizCard(snap, ag.ID, key, r.FormValue(wizCardInput[key]), nil))
}

// wizardDraw draws cards digitally for a step and re-renders its card picker. The table normally
// draws real cards; this is only the backup, same as everywhere else. Contacts draw 3 to pick
// from (p. 41); every other step draws 1.
func (s *Server) wizardDraw(w http.ResponseWriter, r *http.Request) {
	_, ag, snap, err := s.wizardAgent(r)
	if err != nil {
		http.Error(w, friendly(err), http.StatusForbidden)
		return
	}
	key := r.FormValue("for")
	if _, ok := wizCardInput[key]; !ok {
		http.Error(w, "unknown step", http.StatusBadRequest)
		return
	}
	n := 1
	if key == "homeland" || key == "dioscorian" {
		n = 3
	}
	hands, err := cards.Draw(snap, "vision", []cards.Request{{Count: n}})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	drawn := hands[0].Cards
	if n == 1 {
		s.partial(w, "wizard", "wizard-card", newWizCard(snap, ag.ID, key, drawn[0], nil))
		return
	}
	s.partial(w, "wizard", "wizard-card", newWizCard(snap, ag.ID, key, "", drawn))
}

// ---------------------------------------------------------------- names

// wizardNames offers a handful of names from a region's list (given names from the book; family
// names, bynames and Dioscorian names from the homebrew supplement).
func (s *Server) wizardNames(w http.ResponseWriter, r *http.Request) {
	_, _, snap, err := s.wizardAgent(r)
	if err != nil {
		http.Error(w, friendly(err), http.StatusForbidden)
		return
	}
	region := r.FormValue("name_region")
	var d wizSuggestions
	if region != "" {
		names, err := creation.Names(snap, region, 6)
		if err != nil {
			d.Error = sentence(friendly(err))
		}
		for _, n := range names {
			d.Choices = append(d.Choices, wizChoice{Label: n, Fill: map[string]string{"set.name": n, "set.culture": creation.Culture(region)}})
		}
		d.Note = "Given names from the book's list for " + region + "; family names, bynames and Dioscorian names are a homebrew supplement."
	}
	s.partial(w, "wizard", "choices", d)
}

// ---------------------------------------------------------------- Claude's suggestions

// sentence capitalises a message's first letter (Go errors start lower-case).
func sentence(msg string) string {
	if msg == "" {
		return msg
	}
	return strings.ToUpper(msg[:1]) + msg[1:]
}

// wizSuggestions is a set of options to render as choice buttons, plus the hidden "exclude"
// inputs that make the next "more ideas" request skip them.
type wizSuggestions struct {
	Choices []wizChoice
	Exclude []string
	Note    string
	Error   string
	// Card is set when this request drew a fresh card itself (the player hadn't drawn or entered
	// one yet): it's rendered as an out-of-band swap so the drawn card is visible right away,
	// alongside the suggestions inspired by it, not just implied by their "the drawn card" text.
	Card *wizCard
}

var skillPointsRE = regexp.MustCompile(`([A-Za-z]+)\s*\+\s*(\d)`)

// wizardSuggest asks Claude, in the background, for more options at one step. The request
// carries the step's form as the player has it so far (a card they've entered but not saved
// yet, for instance), and what's already on screen so the options are new.
func (s *Server) wizardSuggest(w http.ResponseWriter, r *http.Request) {
	a, ag, snap, err := s.wizardAgent(r)
	if err != nil {
		http.Error(w, friendly(err), http.StatusForbidden)
		return
	}
	_ = r.ParseForm()
	kind := r.FormValue("kind")
	d := wizSuggestions{Exclude: r.Form["exclude"]}
	if s.Assist == nil {
		d.Error = "Suggestions need the in-app assistant, which isn't set up. Pick from the book's options or write your own."
		s.partial(w, "wizard", "choices", d)
		return
	}
	// The hint box on a wizard step is a one-off steer for that step's suggestions, not the
	// Agent's concept: the concept is set once, on the class page, and wizardContext below
	// already carries it into every step's request, so a hint here (e.g. describing a contact)
	// must not overwrite it.
	hint := strings.TrimSpace(r.FormValue("hint"))
	req, bookOptions, drawnCards := wizardContext(a, s, ag, snap, kind, r.Form)
	req.Hint = hint
	req.Exclude = append(slices.Clone(d.Exclude), bookOptions...)

	var sugs []assist.Suggestion
	// sugCards[i], if present, is the card sugs[i] is about — set only when wizardContext drew
	// more than one card (a contact, p. 41: three to pick from), since then each suggestion is
	// its own call about one of the three, not one call inspired by all of them at once.
	var sugCards []string
	if len(drawnCards) > 1 {
		exclude := slices.Clone(req.Exclude)
		var lastErr error
		for _, card := range drawnCards {
			cr := req
			cr.Count = 1
			cr.Exclude = exclude
			if line, _ := cardLine(snap, "Their card", card); line != "" {
				cr.Context = append(slices.Clone(req.Context), line)
			}
			sg, err := s.Assist.Suggest(r.Context(), a.User, cr)
			if err != nil {
				lastErr = err
				continue
			}
			if len(sg) > 0 {
				sugs = append(sugs, sg[0])
				sugCards = append(sugCards, card)
				exclude = append(exclude, sg[0].Fields["name"])
			}
		}
		if len(sugs) == 0 && lastErr != nil {
			d.Error = sentence(friendly(lastErr))
			s.partial(w, "wizard", "choices", d)
			return
		}
	} else {
		sugs, err = s.Assist.Suggest(r.Context(), a.User, req)
		if err != nil {
			d.Error = sentence(friendly(err))
			s.partial(w, "wizard", "choices", d)
			return
		}
	}

	// A card (or cards) wizardContext drew for this request, so the player can see what the
	// suggestions below are actually "in the spirit of" instead of just reading about "the drawn
	// card" without ever seeing which one it was.
	if len(drawnCards) == 1 {
		if forKey := wizCardForKey(kind, r.Form); forKey != "" {
			wc := newWizCard(snap, ag.ID, forKey, drawnCards[0], nil)
			wc.OOB = true
			d.Card = &wc
		}
	} else if len(drawnCards) > 1 {
		if forKey := wizCardForKey(kind, r.Form); forKey != "" {
			wc := newWizCard(snap, ag.ID, forKey, "", drawnCards)
			wc.OOB = true
			d.Card = &wc
		}
	}
	class := snap.Class(ag.Class)
	solo := s.isSolo(a, ag)
	for i, sg := range sugs {
		c, ok := wizChoiceFor(kind, sg, ag, class, snap, solo, r.Form)
		if !ok {
			continue
		}
		// A card wizardContext drew for this request (the player hadn't drawn one yet) needs to
		// land in the form too when the suggestion it inspired is picked, or the "card you drew"
		// box stays empty under an answer that was supposedly drawn from it. When several cards
		// were drawn, each suggestion carries its own (sugCards, parallel to sugs); otherwise
		// every suggestion shares the one card wizardContext drew.
		var card string
		if len(sugCards) == len(sugs) {
			card = sugCards[i]
		} else if len(drawnCards) == 1 {
			card = drawnCards[0]
		}
		if card != "" {
			if field, ok := wizCardInput[kind]; ok {
				c.Fill[field] = card
			}
		}
		d.Choices = append(d.Choices, c)
		d.Exclude = append(d.Exclude, c.Label)
	}
	if len(d.Choices) == 0 {
		d.Error = "Nothing usable came back. Try again, or describe what you're after."
	}
	s.partial(w, "wizard", "choices", d)
}

// formOr is the in-progress form value for field if the form has it, else the saved one.
func formOr(form url.Values, field, saved string) string {
	if vs, ok := form[field]; ok && strings.TrimSpace(vs[len(vs)-1]) != "" {
		return strings.TrimSpace(vs[len(vs)-1])
	}
	return saved
}

func cardLine(snap *gamedata.Snapshot, label, name string) (string, *gamedata.VisionCard) {
	v := findVision(snap, name)
	if v == nil {
		if name == "" {
			return "", nil
		}
		return fmt.Sprintf("%s: %s", label, name), nil
	}
	return fmt.Sprintf("%s: %s — %s. Characters: %s", label, v.Name, v.Meaning, v.Characters), v
}

// wizardContext describes the Agent so far for one step's request, and returns the book options
// already on screen for it (so Claude offers different ones) plus the cards it drew itself, if
// the step draws one and the player hadn't entered one yet (so a picked suggestion can fill it in
// too, instead of leaving the "card you drew" box empty under an answer inspired by a card the
// player never actually saw). Every step draws at most one card of its own except a contact
// (homeland or dioscorian), which draws three to pick from, same as "Draw 3 for me" (p. 41).
func wizardContext(a campaign.Actor, s *Server, ag *db.Agent, snap *gamedata.Snapshot, kind string, form url.Values) (assist.SuggestRequest, []string, []string) {
	req := assist.SuggestRequest{Kind: kind}
	add := func(format string, args ...any) { req.Context = append(req.Context, fmt.Sprintf(format, args...)) }
	class := snap.Class(ag.Class)
	if class != nil {
		add("Class: %s (%s). %s", class.Name, class.Guild, class.Summary)
	}
	if ag.Concept != "" {
		add("The player's own description of this Agent: %s", ag.Concept)
	}
	if n := formOr(form, "set.name", ag.Name); n != "" && n != unnamed {
		add("Name: %s", n)
	}
	if p := formOr(form, "set.child_phrase", ag.ChildPhrase); p != "" {
		add("As a child, I solved problems by %s", p)
	}
	if p := formOr(form, "set.adult_phrase", ag.AdultPhrase); p != "" {
		add("As an adult, I %s by %s", orDefault(formOr(form, "set.adult_verb", ag.AdultVerb), "survived"), p)
	}
	for _, f := range []struct{ label, field, saved string }{
		{"Burden", "set.burden", ag.Burden}, {"Ideal", "set.ideal", ag.Ideal}, {"Culture", "set.culture", ag.Culture},
		{"Age", "set.age", ag.Age}, {"Look", "set.look", ag.Look}, {"Why they came to Dioscoria", "set.why", ag.Why},
	} {
		if v := formOr(form, f.field, f.saved); v != "" && kind != "look" {
			add("%s: %s", f.label, v)
		}
	}
	// Abilities, skills and magic already have their own, more detailed line under their own
	// step below; elsewhere, a short summary lets a later step (e.g. why they came to Dioscoria,
	// or a contact) build on choices made on an earlier or later screen — the player can fill the
	// wizard's steps in any order, so this doesn't assume any of them ran first.
	if kind != "abilities" {
		if abil := resolveAbilities(ag.Abilities, class); len(abil) > 0 {
			var names []string
			for _, ab := range abil {
				names = append(names, ab.Name)
			}
			add("Abilities chosen: %s", strings.Join(names, ", "))
		}
	}
	if kind != "skills" {
		var parts []string
		for _, sk := range snap.Skills.Skills {
			if v := ag.Skills[sk.Name]; v > 0 {
				parts = append(parts, fmt.Sprintf("%s %d", sk.Name, v))
			}
		}
		if len(parts) > 0 {
			add("Skills so far: %s", strings.Join(parts, ", "))
		}
	}
	if kind != "magic" && len(ag.Proficiencies) > 0 {
		var parts []string
		for _, p := range ag.Proficiencies {
			parts = append(parts, fmt.Sprintf("%s (%s)", p.School, p.Rank))
		}
		add("Magic: %s", strings.Join(parts, ", "))
	}

	var book []string
	var drawnCard string
	var drawnCards []string
	// cardFor describes the card for a card-driven field. mayDraw is true only for a step's own
	// single card (child/adult/burden/ideal): if the player hasn't drawn or entered one yet, it
	// draws one itself (the same digital-draw fallback as "Draw for me", cards.Draw) rather than
	// asking Claude to invent phrases "in the spirit of" a card that doesn't exist. "look"'s
	// read-only reuse of earlier steps' cards passes false, since drawing a fresh child/adult/
	// burden/ideal card just to flavor a look suggestion would draw cards those steps never asked
	// for. A contact draws its own three-card fallback below, outside cardFor, since it needs all
	// three back (p. 41), not just the one card cardFor is built to draw and describe.
	cardFor := func(label, field, saved string, options func(*gamedata.VisionCard) []string, mayDraw bool) {
		val := formOr(form, field, saved)
		if val == "" && mayDraw {
			if hands, err := cards.Draw(snap, "vision", []cards.Request{{Count: 1}}); err == nil && len(hands) > 0 && len(hands[0].Cards) > 0 {
				val = hands[0].Cards[0]
				drawnCard = val
			}
		}
		line, v := cardLine(snap, label, val)
		if line == "" {
			return
		}
		add("%s", line)
		if v != nil && options != nil {
			opts := options(v)
			add("The book's options for this card: %s", strings.Join(opts, "; "))
			book = append(book, opts...)
		}
	}
	history := func(v *gamedata.VisionCard) []string { return v.History }
	switch kind {
	case "name":
		if region := form.Get("name_region"); region != "" {
			add("Homeland or culture: %s", region)
		}
	case "child":
		cardFor("Drawn card", "set.child_card", ag.ChildCard, history, true)
	case "adult":
		cardFor("Drawn card", "set.adult_card", ag.AdultCard, history, true)
	case "burden":
		cardFor("Drawn card", "set.burden_card", ag.BurdenCard, func(v *gamedata.VisionCard) []string { return v.Burdens }, true)
	case "ideal":
		cardFor("Drawn card", "set.ideal_card", ag.IdealCard, func(v *gamedata.VisionCard) []string { return v.Ideals }, true)
	case "abilities":
		if class != nil {
			add("Choose %d. The class's abilities:", snap.Limits.Creation.Abilities)
			var pickable []string
			for _, ab := range class.Abilities {
				text := ab.Text
				if len(text) > 400 {
					text = text[:400] + "…"
				}
				add("%s: %s", ab.Name, text)
				pickable = append(pickable, ab.Name)
			}
			req.Enum = map[string][]string{"ability": pickable}
		}
		for _, ab := range resolveAbilities(ag.Abilities, class) {
			book = append(book, ab.Name)
			add("Already chosen: %s", ab.Name)
		}
	case "skills":
		solo := s.isSolo(a, ag)
		cur := wizFormSkills(ag, form)
		total := 0
		var parts []string
		for _, sk := range snap.Skills.Skills {
			total += cur[sk.Name]
			parts = append(parts, fmt.Sprintf("%s %d (%s)", sk.Name, cur[sk.Name], sk.Suit))
		}
		add("Skills now: %s", strings.Join(parts, ", "))
		add("Points to add: %d. Max %d in any skill at creation (p. 41).", max(0, creationTotalPoints(&snap.Limits, solo)-total), creationMaxSkill(&snap.Limits, solo))
	case "magic":
		var names []string
		for _, sc := range creation.MagicSchools(snap) {
			names = append(names, sc.Name)
			if sc.Does != "" {
				add("School %s: %s", sc.Name, sc.Does)
			}
		}
		add("Schools: %s", strings.Join(names, ", "))
		req.Enum = map[string][]string{"school": names}
	case "look":
		for _, c := range []struct{ label, field, saved string }{
			{"Childhood card", "set.child_card", ag.ChildCard}, {"Adulthood card", "set.adult_card", ag.AdultCard},
			{"Burden card", "set.burden_card", ag.BurdenCard}, {"Ideal card", "set.ideal_card", ag.IdealCard},
		} {
			cardFor(c.label, c.field, c.saved, nil, false)
		}
		for _, f := range []struct{ label, field, saved string }{
			{"Culture so far", "set.culture", ag.Culture}, {"Age so far", "set.age", ag.Age}, {"Look so far", "set.look", ag.Look},
		} {
			if v := formOr(form, f.field, f.saved); v != "" {
				add("%s: %s", f.label, v)
			}
		}
	case "why":
		add("The book's examples: %s", strings.Join(creation.WhyReasons, "; "))
		book = append(book, creation.WhyReasons...)
	case "contact":
		kindName := form.Get("set.kind")
		if kindName == "Dioscorian" {
			add("A Dioscorian contact: someone the Agent knows in Dioscoria, at affection 1 (p. 41).")
		} else {
			add("A homeland contact: someone from the Agent's homeland (p. 41).")
		}
		if land := form.Get("set.land"); land != "" {
			add("Their land: %s", land)
		}
		if val := formOr(form, "set.card", ""); val != "" {
			cardFor("Their card", "set.card", "", nil, false)
		} else if hands, err := cards.Draw(snap, "vision", []cards.Request{{Count: 3}}); err == nil && len(hands) > 0 {
			// Three to pick from, same as "Draw 3 for me" (p. 41) — not the single-card draw
			// cardFor does for the other card-driven steps.
			drawnCards = hands[0].Cards
		}
	case "ability_contact":
		if name := form.Get("ability_name"); name != "" {
			add("This ability grants a contact: %s (p. 43).", name)
		}
	}
	if drawnCard != "" {
		drawnCards = append(drawnCards, drawnCard)
	}
	return req, book, drawnCards
}

// wizFormSkills is the Agent's skills with any unsaved changes on the skills form applied.
func wizFormSkills(ag *db.Agent, form url.Values) map[string]int {
	cur := copyMap(ag.Skills)
	for key, vals := range form {
		if name, ok := strings.CutPrefix(key, "skill."); ok {
			if n, err := strconv.Atoi(vals[len(vals)-1]); err == nil {
				cur[name] = n
			}
		}
	}
	return cur
}

// wizChoiceFor turns one of Claude's options into something the player can pick, checking it
// against the rules data (an ability or class that doesn't exist is dropped, not shown; a skill
// suggestion is clamped to what the creation limits actually allow, not just what was asked for).
func wizChoiceFor(kind string, sg assist.Suggestion, ag *db.Agent, class *gamedata.Class, snap *gamedata.Snapshot, solo bool, form url.Values) (wizChoice, bool) {
	f := sg.Fields
	c := wizChoice{Why: sg.Why, Fill: map[string]string{}}
	switch kind {
	case "class":
		cl := snap.Class(f["class"])
		if cl == nil {
			for i := range snap.Classes.Classes {
				if strings.EqualFold(snap.Classes.Classes[i].Name, f["class"]) {
					cl = &snap.Classes.Classes[i]
				}
			}
		}
		if cl == nil {
			return c, false
		}
		c.Label, c.ClassID = cl.Name, cl.ID
	case "name":
		c.Label, c.Fill["set.name"] = f["name"], f["name"]
	case "child":
		c.Label, c.Fill["set.child_phrase"] = f["phrase"], f["phrase"]
	case "adult":
		verb := strings.ToLower(f["verb"])
		if verb != "survived" && verb != "flourished" {
			verb = "survived"
		}
		c.Label = verb + " by " + f["phrase"]
		c.Fill["set.adult_verb"], c.Fill["set.adult_phrase"] = verb, f["phrase"]
	case "burden":
		c.Label, c.Fill["set.burden"] = f["word"], f["word"]
	case "ideal":
		c.Label, c.Fill["set.ideal"] = f["word"], f["word"]
	case "why":
		c.Label, c.Fill["set.why"] = f["sentence"], f["sentence"]
	case "look":
		c.Label = f["look"] + " (" + f["age"] + ", " + f["culture"] + ")"
		c.Fill["set.age"], c.Fill["set.culture"], c.Fill["set.look"] = f["age"], f["culture"], f["look"]
	case "contact":
		c.Label = f["name"] + " — " + f["description"]
		c.Fill["set.name"], c.Fill["set.land"], c.Fill["set.description"] = f["name"], f["land"], f["description"]
	case "ability_contact":
		c.Label = f["name"] + " — " + f["description"]
		c.Fill["name"], c.Fill["description"] = f["name"], f["description"]
	case "magic":
		for _, sc := range creation.MagicSchools(snap) {
			if strings.EqualFold(sc.Name, f["school"]) {
				c.Label, c.Fill["prof.add"] = sc.Name, sc.Name
			}
		}
		if c.Label == "" {
			return c, false
		}
	case "abilities":
		if class == nil {
			return c, false
		}
		for _, ab := range class.Abilities {
			if strings.EqualFold(ab.Name, f["ability"]) {
				c.Label, c.AbilityID = ab.Name, ab.ID
			}
		}
		for _, have := range ag.Abilities {
			if have.ID != "" && have.ID == c.AbilityID {
				return c, false
			}
		}
		if c.AbilityID == "" {
			return c, false
		}
	case "skills":
		cur := wizFormSkills(ag, form)
		total := 0
		for _, v := range cur {
			total += v
		}
		remaining := creationTotalPoints(&snap.Limits, solo) - total
		maxSkill := creationMaxSkill(&snap.Limits, solo)
		known := map[string]string{}
		for _, sk := range snap.Skills.Skills {
			known[strings.ToLower(sk.Name)] = sk.Name
		}
		added := map[string]int{}
		var order []string
		// place puts as much of n into name as the cap and what's left actually allow.
		place := func(name string, n int) {
			if room := maxSkill - cur[name]; n > room {
				n = room
			}
			if n > remaining {
				n = remaining
			}
			if n <= 0 {
				return
			}
			if added[name] == 0 {
				order = append(order, name)
			}
			cur[name] += n
			added[name] += n
			remaining -= n
		}
		for _, m := range skillPointsRE.FindAllStringSubmatch(f["points"], -1) {
			if name, ok := known[strings.ToLower(m[1])]; ok {
				n, _ := strconv.Atoi(m[2])
				place(name, n)
			}
		}
		// A suggestion is a starting point, not the rules: the model doesn't always add up to the
		// points remaining, or it names a skill already at (or near) the cap it was given, which
		// used to just quietly come up short. Spend whatever's left on the skills it already
		// named (most likely still in the spirit of the suggestion), and only then, deterministically,
		// on any other skill still under cap — so a suggestion always uses every point that's
		// actually still placeable, rather than silently offering less than it claims.
		for _, name := range order {
			if remaining <= 0 {
				break
			}
			place(name, remaining)
		}
		if remaining > 0 {
			for _, sk := range snap.Skills.Skills {
				if remaining <= 0 {
					break
				}
				place(sk.Name, remaining)
			}
		}
		if len(order) == 0 {
			return c, false
		}
		var parts []string
		for _, name := range order {
			c.Fill["skill."+name] = strconv.Itoa(cur[name])
			parts = append(parts, fmt.Sprintf("%s +%d", name, added[name]))
		}
		c.Label = strings.Join(parts, ", ")
	default:
		return c, false
	}
	if strings.TrimSpace(c.Label) == "" {
		return c, false
	}
	return c, true
}
