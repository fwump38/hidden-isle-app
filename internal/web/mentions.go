package web

import (
	"fmt"
	"html/template"
	"regexp"
	"strconv"
	"strings"
)

// @mentions: a body of free-form text can carry a token @[Display Name](kind:id), inserted by
// the picker in app.js (GET /c/{cid}/mentions backs its dropdown). Storage is plain text, so the
// export and the MCP tools see it unchanged; mentions turns it into a link when a page renders
// that text. Nothing here checks live permissions — the token's own display name was already
// chosen by someone who could see that target when they typed it, exactly as if they'd typed the
// name in plain text; the link itself is just app navigation, gated the normal way when opened.
//
// kind is one of:
//   - "agent": id is the Agent's id → /agents/{id}
//   - "contact": id is "{agent id}.{contact id}" (a contact has no page of its own) → the Agent's
//     sheet, anchored at that contact
//   - "adversary" / "territory": id is the record's id → that section's page, anchored at it
//   - "user": id is the user's id; rendered as a plain badge, never a link (no public profile
//     page to send it to)
//
// An unrecognised kind, or an id that doesn't parse, renders as the plain display name: broken
// markup never reaches the page.
var mentionRE = regexp.MustCompile(`@\[([^\]\n]{1,80})\]\((\w+):([\w.]{1,40})\)`)

// mentions turns a body of text into HTML with its @mention tokens linked, for a page in
// campaignID (adversary/territory links need it; other kinds don't).
func mentions(body string, campaignID uint) template.HTML {
	var b strings.Builder
	last := 0
	for _, m := range mentionRE.FindAllStringSubmatchIndex(body, -1) {
		b.WriteString(template.HTMLEscapeString(body[last:m[0]]))
		name, kind, id := body[m[2]:m[3]], body[m[4]:m[5]], body[m[6]:m[7]]
		b.WriteString(mentionLink(kind, id, name, campaignID))
		last = m[1]
	}
	b.WriteString(template.HTMLEscapeString(body[last:]))
	return template.HTML(b.String())
}

func mentionLink(kind, id, name string, campaignID uint) string {
	label := "@" + template.HTMLEscapeString(name)
	switch kind {
	case "agent":
		if n, err := strconv.ParseUint(id, 10, 64); err == nil {
			return fmt.Sprintf(`<a class="hi-mention" href="/agents/%d">%s</a>`, n, label)
		}
	case "contact":
		if aid, cid, ok := strings.Cut(id, "."); ok {
			if a, err1 := strconv.ParseUint(aid, 10, 64); err1 == nil {
				if c, err2 := strconv.ParseUint(cid, 10, 64); err2 == nil {
					return fmt.Sprintf(`<a class="hi-mention" href="/agents/%d#contact-%d">%s</a>`, a, c, label)
				}
			}
		}
	case "adversary":
		if n, err := strconv.ParseUint(id, 10, 64); err == nil {
			return fmt.Sprintf(`<a class="hi-mention" href="/c/%d/adversaries#adversary-%d">%s</a>`, campaignID, n, label)
		}
	case "territory":
		if n, err := strconv.ParseUint(id, 10, 64); err == nil {
			return fmt.Sprintf(`<a class="hi-mention" href="/c/%d/territories#territory-%d">%s</a>`, campaignID, n, label)
		}
	case "user":
		return fmt.Sprintf(`<span class="hi-mention hi-mention-user">%s</span>`, label)
	}
	return label
}

// plainMentions strips a body's @mention tokens down to their plain display name, for output
// that isn't a rendered web page (the Markdown/JSON export).
func plainMentions(body string) string {
	return mentionRE.ReplaceAllString(body, "@$1")
}
