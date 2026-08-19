package main

// `crewship agent ask-preview` — render one of an agent's ask forms without a
// browser.
//
// House rule is that every endpoint gets a CLI command. This one is the
// inverse case and worth stating: ask forms deliberately have NO endpoint of
// their own — they ride the agent PATCH, exactly as suggested_prompts does —
// so what the CLI adds is not a wrapper but the only way to answer the
// question an author actually has, which is "what does this template
// produce?".
//
// Without it, testing a template means opening a chat, clicking a chip,
// filling a sheet and reading a preview pane, and the template is usually
// wrong on the first try. With it:
//
//	crewship agent ask-preview lucy receipt --var supplier=Vodafone \
//	  --var amount=1249 --var amount_currency=CZK --var document=IMG_4821.heic
//
// It renders through internal/askforms — the SAME renderer the server uses,
// pinned to lib/ask-template.ts by testdata/ask-templates.json — so what it
// prints is what the composer would send, not an approximation of it.

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/askforms"
	"github.com/crewship-ai/crewship/internal/cli"
)

var agentAskPreviewCmd = &cobra.Command{
	Use:   "ask-preview <agent> <form-id>",
	Short: "Render one of an agent's ask forms with sample answers",
	Long: "Render an ask form's prompt template with the answers given as --var, " +
		"and print the message that would be sent.\n\n" +
		"Values are given as --var name=value, repeated. A repeated name becomes a " +
		"multi-value (several files, or a multiselect). A money field named `amount` " +
		"takes its currency as `amount_currency`.\n\n" +
		"An empty optional value drops the whole line it sits on, as long as no other " +
		"placeholder on that line produced anything — which is the one rule worth " +
		"testing here before anyone meets it in a chat.",
	Args:              cobra.ExactArgs(2),
	ValidArgsFunction: completeAgentSlug,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		client := newAPIClient()
		resp, _, err := getByRef(client, "/api/v1/agents/", args[0], resolveAgentID)
		if err != nil {
			return err
		}
		var agent agentDetailResponse
		if err := cli.ReadJSON(resp, &agent); err != nil {
			return err
		}

		raw := ""
		if agent.AskForms != nil {
			raw = *agent.AskForms
		}
		forms, err := askforms.Parse(raw)
		if err != nil {
			// Stored definitions were validated on write, so this means the
			// column was edited around the API. Say so rather than printing
			// half a message.
			return fmt.Errorf("this agent's stored ask forms are not valid: %w", err)
		}
		if len(forms) == 0 {
			return fmt.Errorf("agent %s has no ask forms configured — set them with "+
				"`crewship agent update %s --ask-forms @forms.json`", args[0], args[0])
		}

		varFlags, _ := cmd.Flags().GetStringArray("var")
		values, err := parseAskVars(varFlags)
		if err != nil {
			return err
		}
		chatID, _ := cmd.Flags().GetString("chat-id")

		form, found := askforms.FormByID(forms, args[1])
		if !found {
			// RenderByID owns this error: it names the id asked for AND the
			// ids that exist, which is the second lookup an operator would
			// otherwise do by hand.
			_, err := askforms.RenderByID(forms, args[1], values, chatID)
			return err
		}

		// The same constraints the sheet applies at submit (internal/askforms
		// /answers.go). A preview whose whole promise is "this is what gets
		// sent" must not print a message the console would have refused —
		// that is the P0.7 gap seen from the other end.
		if problems := askforms.ValidateAnswers(form, values); len(problems) > 0 {
			lines := make([]string, 0, len(problems))
			for _, p := range problems {
				lines = append(lines, "  "+p.Message)
			}
			return fmt.Errorf("these answers would be refused in the chat:\n%s",
				strings.Join(lines, "\n"))
		}

		// Belt to the validator's braces, for a definition stored before the
		// field-type rule existed: an unsafe-typed answer is dropped rather
		// than rendered into something pipeable.
		message := askforms.Render(form, askforms.SanitizeValues(form, values), chatID)

		// Plain stdout, no framing. The point of a preview is to be diffable
		// and pipeable — into a file, into `wc -c`, into a review comment.
		fmt.Println(message)
		return nil
	},
}

// parseAskVars turns repeated --var name=value into the answers map.
//
// A repeated name accumulates into a list rather than overwriting, because
// that is the only shape that can express the two multi-valued field kinds
// (several attachments, a multiselect) and because silently keeping the last
// of two --var document= flags would drop a file the user asked to send.
func parseAskVars(pairs []string) (askforms.Values, error) {
	values := askforms.Values{}
	for _, pair := range pairs {
		name, value, found := strings.Cut(pair, "=")
		if !found || strings.TrimSpace(name) == "" {
			return nil, fmt.Errorf("--var %q is not name=value", pair)
		}
		name = strings.TrimSpace(name)
		switch existing := values[name].(type) {
		case nil:
			values[name] = value
		case string:
			values[name] = []string{existing, value}
		case []string:
			values[name] = append(existing, value)
		}
	}
	return values, nil
}

func init() {
	agentAskPreviewCmd.Flags().StringArray("var", nil,
		"Answer one field: name=value. Repeat for more fields, and repeat a name for a multi-value")
	agentAskPreviewCmd.Flags().String("chat-id", "preview",
		"Chat id used to build attachments/<chat-id>/<file> paths in the rendered message")

	agentCmd.AddCommand(agentAskPreviewCmd)
}
