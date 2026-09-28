package web

// assistField describes one free-form text field that the writing assistant can help with:
// what it's called in the prompt, extra per-field guidance, whether it needs an Agent in scope
// (for its own contacts and history), whether the text may end up in front of players (the leak
// guard in buildBrief), and whether it can be drafted from campaign data alone with nothing
// written yet.
type assistField struct {
	Label      string // used in the prompt, e.g. "journal entry"
	Guide      string // extra instructions, e.g. a length hint
	NeedsAgent bool   // pull in the Agent named by "ref" (an Agent id)
	SeerOnly   bool   // only the Seer may ask for this field
	Public     bool   // the result may reach players: strip Seer-only context even for the Seer
	AllowDraft bool   // offer "Draft from campaign" as well as "Enhance"
}

// assistFields is keyed by the field name each write-assist box passes as "field". Add an entry
// here and a {{template "write-assist" …}} call in the template to give a textarea AI help.
var assistFields = map[string]assistField{
	// Journal and entries (players and the Seer; NeedsAgent lets a journal draw on its author's
	// own Agent when one is in scope, e.g. from the sheet's own history box).
	"journal":       {Label: "journal entry", Guide: "Write in the first person, as the Agent or the player."},
	"session_entry": {Label: "session entry", Guide: "Write in the first person, as the Agent or the player."},
	"agent_history": {Label: "Agent history entry", NeedsAgent: true, Guide: "One or two sentences, in the third person."},

	// Session (Seer-only).
	"session_summary": {Label: "session summary", SeerOnly: true, Public: true, AllowDraft: true,
		Guide: "The player-safe version, as it will be read aloud. Don't reveal anything Seer-only."},
	"session_prep":       {Label: "session prep", SeerOnly: true, Guide: "Cast, locations, clocks, likely challenges, twists."},
	"session_divination": {Label: "divination notes", SeerOnly: true, Guide: "Adversary, stakes, goal, lead, visions given and kept."},
	"session_next_time":  {Label: "notes for next time", SeerOnly: true, AllowDraft: true},

	// Play page (Seer-only).
	"handout": {Label: "handout", SeerOnly: true, Public: true, Guide: "Text the players will read aloud or see verbatim."},

	// Records (Seer-only).
	"seer_note":            {Label: "Seer note", SeerOnly: true},
	"adversary_plot":       {Label: "adversary's plot", SeerOnly: true},
	"adversary_motivation": {Label: "adversary's motivation", SeerOnly: true},
	"adversary_secrets":    {Label: "adversary's secrets", SeerOnly: true},
	"adversary_members":    {Label: "adversary's members", SeerOnly: true, Guide: "Up to 4, each with a short motivation."},
	"territory_events":     {Label: "territory's events", SeerOnly: true},
	"territory_contacts":   {Label: "territory's contacts", SeerOnly: true},
	"territory_notes":      {Label: "territory's notes", SeerOnly: true},

	// Agent sheet (the owner or the Seer).
	"agent_look":   {Label: "Agent's look", NeedsAgent: true, Guide: "One or two sentences."},
	"agent_why":    {Label: "why the Agent came to Dioscoria", NeedsAgent: true, Guide: "One or two sentences."},
	"agent_notes":  {Label: "Agent's notes", NeedsAgent: true},
	"ability_text": {Label: "custom ability's text", NeedsAgent: true, Guide: "Keep it rules-light, in the style of the book's ability text."},

	// Contacts (the owner, the Seer, or a player during downtime/wizard).
	"contact_description": {Label: "contact's description", NeedsAgent: true, Guide: "Profession and personality, one or two sentences."},

	// Downtime (the player).
	"downtime_vignette": {Label: "downtime vignette", NeedsAgent: true, Guide: "A short scene, one or two sentences."},
	"downtime_note":     {Label: "note for the Seer", NeedsAgent: true},

	// The shared change-log reason.
	"why": {Label: "reason for this change", Guide: "One short line."},

	// Campaign settings (Seer-only).
	"campaign_table": {Label: "table agreements", SeerOnly: true, Public: true,
		Guide: "Session length and cadence, tone, lines (never) and veils (off-screen)."},
	"campaign_open_threads": {Label: "open threads", SeerOnly: true, Public: true, AllowDraft: true,
		Guide: "Loose ends to pick up later: unfulfilled visions, adversary schemes, hooks from downtime."},
}
