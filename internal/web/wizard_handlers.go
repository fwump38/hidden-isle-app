package web

import (
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/fwump38/hidden-isle-app/internal/campaign"
	"github.com/fwump38/hidden-isle-app/internal/cards"
	"github.com/fwump38/hidden-isle-app/internal/chat"
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
	Agent       *db.Agent
	Class       *gamedata.Class
	Solo        bool
	Limits      *gamedata.Limits
	Step        int
	Key         string
	Steps       []wizStepInfo
	Done        map[int]bool
	Vision      []string
	Homeland    *db.Contact
	Dioscorian  *db.Contact
	Chosen      map[string]bool // ability id -> already on the sheet
	Abil        []wizardAbility // the Agent's chosen abilities, resolved to their name and text
	ChatEnabled bool            // Claude's background suggestions are available
	Regions     []string        // where names can come from
	Schools     []creation.School
	WhyReasons  []string
	Skills      map[string][]string // suit -> skill names
	Cards       map[string]wizCard  // the card pickers on this screen, by step key
	Error       string
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
	Index int
	Name  string
	Text  string
}

func resolveAbilities(abilities []db.AgentAbility, class *gamedata.Class) []wizardAbility {
	var out []wizardAbility
	for i, ab := range abilities {
		v := wizardAbility{Index: i, Name: ab.Name, Text: ab.Text}
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

// creationStep works out the first incomplete step for ag (1-13; 13 = everything's there).
func creationStep(ag *db.Agent, class *gamedata.Class, l *gamedata.Limits, solo bool, contacts []db.Contact) int {
	switch {
	case strings.TrimSpace(ag.Name) == "" || ag.Name == unnamed:
		return 1
	case strings.TrimSpace(ag.ChildPhrase) == "":
		return 2
	case strings.TrimSpace(ag.AdultPhrase) == "" || ag.AdultVerb == "":
		return 3
	case strings.TrimSpace(ag.Burden) == "":
		return 4
	case strings.TrimSpace(ag.Ideal) == "":
		return 5
	case len(ag.Abilities) < l.Creation.Abilities:
		return 6
	case skillTotal(ag) < creationTotalPoints(l, solo):
		return 7
	}
	if class != nil && class.StartsWithAdeptProficiency && len(ag.Proficiencies) == 0 {
		return 8
	}
	switch {
	case strings.TrimSpace(ag.Look) == "":
		return 9
	case strings.TrimSpace(ag.Why) == "":
		return 10
	}
	if !hasContactKind(contacts, "Homeland") {
		return 11
	}
	if !hasContactKind(contacts, "Dioscorian") {
		return 12
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

func (s *Server) buildWizardData(a campaign.Actor, ag *db.Agent, snap *gamedata.Snapshot) wizardData {
	class := snap.Class(ag.Class)
	solo := false
	if ag.CampaignID != 0 {
		if c, err := s.Svc.Campaign(a, ag.CampaignID); err == nil && c.Mode == "solitaire" {
			solo = true
		}
	}
	contacts, _ := s.Svc.Contacts(a, ag.ID)
	chosen := map[string]bool{}
	for _, ab := range ag.Abilities {
		if ab.ID != "" {
			chosen[ab.ID] = true
		}
	}
	d := wizardData{Agent: ag, Class: class, Solo: solo, Limits: &snap.Limits, Steps: wizStepList,
		Done: map[int]bool{}, Homeland: findContact(contacts, "Homeland"), Dioscorian: findContact(contacts, "Dioscorian"),
		Chosen: chosen, Abil: resolveAbilities(ag.Abilities, class), ChatEnabled: s.Chat != nil,
		Regions: creation.NameRegions(snap), Schools: creation.MagicSchools(snap), WhyReasons: creation.WhyReasons,
		Skills: map[string][]string{}}
	current := creationStep(ag, class, &snap.Limits, solo, contacts)
	for _, st := range wizStepList {
		d.Done[st.N] = st.N < current
	}
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
	if s.Chat == nil {
		d.Error = "Suggestions need the in-app assistant, which isn't set up. Pick from the book's options or write your own."
		s.partial(w, "wizard", "choices", d)
		return
	}
	req, bookOptions := wizardContext(a, s, ag, snap, kind, r.Form)
	req.Hint = r.FormValue("hint")
	req.Exclude = append(slices.Clone(d.Exclude), bookOptions...)
	sugs, err := s.Chat.Suggest(r.Context(), a.User, req)
	if err != nil {
		d.Error = sentence(friendly(err))
		s.partial(w, "wizard", "choices", d)
		return
	}
	class := snap.Class(ag.Class)
	for _, sg := range sugs {
		if c, ok := wizChoiceFor(kind, sg, ag, class, snap, r.Form); ok {
			d.Choices = append(d.Choices, c)
			d.Exclude = append(d.Exclude, c.Label)
		}
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
// already on screen for it (so Claude offers different ones).
func wizardContext(a campaign.Actor, s *Server, ag *db.Agent, snap *gamedata.Snapshot, kind string, form url.Values) (chat.SuggestRequest, []string) {
	req := chat.SuggestRequest{Kind: kind}
	add := func(format string, args ...any) { req.Context = append(req.Context, fmt.Sprintf(format, args...)) }
	class := snap.Class(ag.Class)
	if class != nil {
		add("Class: %s (%s). %s", class.Name, class.Guild, class.Summary)
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

	var book []string
	cardFor := func(label, field, saved string, options func(*gamedata.VisionCard) []string) {
		line, v := cardLine(snap, label, formOr(form, field, saved))
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
		cardFor("Drawn card", "set.child_card", ag.ChildCard, history)
	case "adult":
		cardFor("Drawn card", "set.adult_card", ag.AdultCard, history)
	case "burden":
		cardFor("Drawn card", "set.burden_card", ag.BurdenCard, func(v *gamedata.VisionCard) []string { return v.Burdens })
	case "ideal":
		cardFor("Drawn card", "set.ideal_card", ag.IdealCard, func(v *gamedata.VisionCard) []string { return v.Ideals })
	case "abilities":
		if class != nil {
			add("Choose %d. The class's abilities:", snap.Limits.Creation.Abilities)
			for _, ab := range class.Abilities {
				text := ab.Text
				if len(text) > 400 {
					text = text[:400] + "…"
				}
				add("%s: %s", ab.Name, text)
			}
		}
		for _, ab := range resolveAbilities(ag.Abilities, class) {
			book = append(book, ab.Name)
			add("Already chosen: %s", ab.Name)
		}
	case "skills":
		solo := false
		if ag.CampaignID != 0 {
			if c, err := s.Svc.Campaign(a, ag.CampaignID); err == nil && c.Mode == "solitaire" {
				solo = true
			}
		}
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
	case "look":
		for _, c := range []struct{ label, field, saved string }{
			{"Childhood card", "set.child_card", ag.ChildCard}, {"Adulthood card", "set.adult_card", ag.AdultCard},
			{"Burden card", "set.burden_card", ag.BurdenCard}, {"Ideal card", "set.ideal_card", ag.IdealCard},
		} {
			cardFor(c.label, c.field, c.saved, nil)
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
		cardFor("Their card", "set.card", "", nil)
	}
	return req, book
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
// against the rules data (an ability or class that doesn't exist is dropped, not shown).
func wizChoiceFor(kind string, sg chat.Suggestion, ag *db.Agent, class *gamedata.Class, snap *gamedata.Snapshot, form url.Values) (wizChoice, bool) {
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
		c.Label, c.Fill["set.why"] = f["why"], f["why"]
	case "look":
		c.Label = f["look"] + " (" + f["age"] + ", " + f["culture"] + ")"
		c.Fill["set.age"], c.Fill["set.culture"], c.Fill["set.look"] = f["age"], f["culture"], f["look"]
	case "contact":
		c.Label = f["name"] + " — " + f["description"]
		c.Fill["set.name"], c.Fill["set.land"], c.Fill["set.description"] = f["name"], f["land"], f["description"]
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
		known := map[string]string{}
		for _, sk := range snap.Skills.Skills {
			known[strings.ToLower(sk.Name)] = sk.Name
		}
		var parts []string
		for _, m := range skillPointsRE.FindAllStringSubmatch(f["points"], -1) {
			name, ok := known[strings.ToLower(m[1])]
			if !ok {
				continue
			}
			n, _ := strconv.Atoi(m[2])
			cur[name] += n
			c.Fill["skill."+name] = strconv.Itoa(cur[name])
			parts = append(parts, fmt.Sprintf("%s +%d", name, n))
		}
		if len(parts) == 0 {
			return c, false
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
