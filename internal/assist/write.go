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

// WriteMode says what Write is being asked to do.
type WriteMode string

const (
	// ModeEnhance takes what the author already wrote and adds detail and description that fits
	// the setting, without changing what it says.
	ModeEnhance WriteMode = "enhance"
	// ModeDraft writes new text from campaign data alone (a recap, open threads…), with no
	// existing text to build on. It only ever describes events the brief actually lists.
	ModeDraft WriteMode = "draft"
)

// WriteRequest is one writing-assistant call for a single field.
type WriteRequest struct {
	Field string // a label for the field, e.g. "journal entry" (goes straight into the prompt)
	Mode  WriteMode
	Text  string // the author's own draft (ModeEnhance) or "" (ModeDraft)
	Brief *Brief // campaign context, already permission-filtered by the caller
	Guide string // extra per-field instructions, e.g. "one or two sentences"
}

// ErrWriteUnavailable is what the caller sees when the API call itself fails.
var ErrWriteUnavailable = errors.New("writing help isn't available right now; edit it yourself")

const writeTokens = 700

const writeInstructions = `You help someone write for The Hidden Isle, a tarot RPG of sorcery and adventure set in 1562. You only ever answer through the offer_text tool.

- Keep the author's own voice, point of view and every fact they stated; you add period detail and description, you don't rewrite their meaning.
- Use only names, places and events given in the campaign details below; never invent a rule, a number, or an event that isn't there.
- Keep any @mention token (` + "`@[Name](kind:id)`" + `) exactly as written, in the same place.
- Match the length of what's already there; don't pad it out for its own sake.
- If asked to draft from campaign data alone, describe only what the details actually say happened; don't invent a scene.`

// Write asks Claude to enhance the author's own text, or (ModeDraft) to draft new text purely
// from the brief. It never sees or touches the database itself: the caller saves the result (or
// not) exactly like every other edit.
func (s *Service) Write(ctx context.Context, u *db.User, req WriteRequest) (string, error) {
	if err := s.checkRateIn(s.recentSuggest, u.ID, suggestRateCount); err != nil {
		return "", err
	}
	month := currentMonth()
	userUSD, globalUSD := s.monthSpend(month, u.ID)
	if s.cfg.PlayerCapUSD > 0 && userUSD >= s.cfg.PlayerCapUSD {
		return "", fmt.Errorf("%w (your monthly cap)", ErrBudget)
	}
	if s.cfg.GlobalCapUSD > 0 && globalUSD >= s.cfg.GlobalCapUSD {
		return "", fmt.Errorf("%w (the table's monthly cap)", ErrBudget)
	}
	if req.Mode == ModeEnhance && strings.TrimSpace(req.Text) == "" {
		return "", errors.New("write something first")
	}

	var b strings.Builder
	switch req.Mode {
	case ModeDraft:
		fmt.Fprintf(&b, "Draft the %s from the campaign details below. Don't invent anything not listed.\n", req.Field)
	default:
		fmt.Fprintf(&b, "Enhance this %s: add sensory and period detail that fits, without changing what it says.\n", req.Field)
	}
	if req.Guide != "" {
		fmt.Fprintf(&b, "%s\n", req.Guide)
	}
	if lines := req.Brief.Lines(); len(lines) > 0 {
		b.WriteString("\nCampaign details:\n")
		for _, l := range lines {
			fmt.Fprintf(&b, "- %s\n", l)
		}
	}
	if req.Mode != ModeDraft {
		fmt.Fprintf(&b, "\nWhat's written so far:\n%s\n", req.Text)
	}

	tool := newTool("offer_text", "Offer the rewritten (or drafted) text.", map[string]any{
		"text": strProp("The full text, ready to use as-is"),
	}, "text")

	resp, err := s.client.Messages.New(ctx, anthropic.MessageNewParams{
		Model: anthropic.Model(s.cfg.Model), MaxTokens: writeTokens,
		System:     []anthropic.TextBlockParam{{Text: writeInstructions, CacheControl: anthropic.NewCacheControlEphemeralParam()}},
		Messages:   []anthropic.MessageParam{anthropic.NewUserMessage(anthropic.NewTextBlock(b.String()))},
		Tools:      []anthropic.ToolUnionParam{tool},
		ToolChoice: anthropic.ToolChoiceParamOfTool("offer_text"),
	})
	if err != nil {
		slog.Warn("writing assistant", "field", req.Field, "mode", req.Mode, "err", err)
		return "", ErrWriteUnavailable
	}
	s.recordUsage(month, u.ID, resp.Usage.InputTokens+resp.Usage.CacheCreationInputTokens+resp.Usage.CacheReadInputTokens, resp.Usage.OutputTokens)

	for _, blk := range resp.Content {
		if blk.Type != "tool_use" || blk.Name != "offer_text" {
			continue
		}
		var out struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal(blk.Input, &out); err != nil {
			return "", fmt.Errorf("couldn't read the result: %w", err)
		}
		if strings.TrimSpace(out.Text) == "" {
			return "", errors.New("nothing usable came back; try again")
		}
		return strings.TrimSpace(out.Text), nil
	}
	return "", errors.New("nothing usable came back; try again")
}
