package assist

import (
	"fmt"
	"strings"
)

// Brief is campaign context assembled for a writing-assistant or suggestion request: an ordered
// list of plain-text lines. The web layer builds one (internal/web/assist_context.go) through
// campaign.Service with the caller's Actor, so the same permission filtering that gates the rest
// of the app decides what a request can see. When the text being written is one a player might
// read (a recap, a handout, open threads…), the builder leaves out anything Seer-only even when
// the Seer is the one asking, so nothing secret can leak into player-facing prose.
type Brief struct {
	lines []string
}

// Add appends one context line, dropping it if it would be blank.
func (b *Brief) Add(format string, args ...any) {
	line := strings.TrimSpace(fmt.Sprintf(format, args...))
	if line != "" {
		b.lines = append(b.lines, line)
	}
}

// Lines returns the brief's context lines, in the order they were added.
func (b *Brief) Lines() []string {
	if b == nil {
		return nil
	}
	return b.lines
}
