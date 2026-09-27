package mcpsrv

import (
	"context"
	"strings"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/fwump38/hidden-isle-app/internal/gamedata"
)

// The plugin's skills, served as MCP prompts from the rules data, so a claude.ai chat with only
// the connector (no plugin installed) still gets create-agent, challenge, downtime and the rest.

var promptMu sync.Mutex

// syncPrompts replaces the registered prompts with the snapshot's skills.
func (s *Server) syncPrompts(snap *gamedata.Snapshot) {
	if snap == nil {
		return
	}
	promptMu.Lock()
	defer promptMu.Unlock()
	if len(s.prompts) > 0 {
		s.mcp.RemovePrompts(s.prompts...)
	}
	s.prompts = nil
	for _, p := range snap.Prompts {
		p := p
		arg := &mcp.PromptArgument{Name: "request", Description: "What you want, in your own words"}
		if p.ArgumentHint != "" {
			arg.Description += " (e.g. " + p.ArgumentHint + ")"
		}
		s.mcp.AddPrompt(&mcp.Prompt{Name: p.Name, Title: title(p.Name), Description: p.Description, Arguments: []*mcp.PromptArgument{arg}},
			func(ctx context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
				text := "Follow this Hidden Isle skill, using the Hidden Isle connector's tools for campaign state and (if no rules files are attached) search_rules/read_rules for the rules.\n\n" + p.Body
				if r := strings.TrimSpace(req.Params.Arguments["request"]); r != "" {
					text += "\n\n---\nThe request: " + r
				}
				return &mcp.GetPromptResult{Description: p.Description,
					Messages: []*mcp.PromptMessage{{Role: "user", Content: &mcp.TextContent{Text: text}}}}, nil
			})
		s.prompts = append(s.prompts, p.Name)
	}
}

func title(name string) string {
	words := strings.Split(name, "-")
	for i, w := range words {
		if w != "" {
			words[i] = strings.ToUpper(w[:1]) + w[1:]
		}
	}
	return "Hidden Isle: " + strings.Join(words, " ")
}
