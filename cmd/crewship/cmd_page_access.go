package main

// `crewship page access` — who reaches a page and how, or which pages one
// subject reaches (docs/prd/pages-collections-access-analysis-2026-09-12.md
// §4, §5/10, §6; F2 of §10).
//
//	GET /api/v1/pages/{slug}/access   page access <slug>
//	GET /api/v1/pages/access          page access --subject user:<id>|crew:<slug>|agent:<slug>
//
// One command, two questions, and exactly one of them per invocation: a slug
// asks "who reaches this page", --subject asks "what does this subject
// reach". Both print the server's paths verbatim — `owner`, `role`,
// `crew:<slug>`, `panel_crew:<slug>`, `grant:page:<level>` — because the
// vocabulary is the API's and a client that reworded it would be a second
// place for it to drift. A row whose subject reads `withheld` is the server
// declining to name a crew you cannot see; there is nothing to ask for.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

// ── The wire (mirrors internal/api/pages_access.go) ────────────────────────

type pageAccessSubjectJSON struct {
	SubjectType string   `json:"subject_type"`
	SubjectID   string   `json:"subject_id"`
	Label       string   `json:"label"`
	Paths       []string `json:"paths"`
}

type pageAccessJSON struct {
	Page       string                  `json:"page"`
	Subjects   []pageAccessSubjectJSON `json:"subjects"`
	NextCursor string                  `json:"next_cursor"`
}

type pageSubjectAccessJSON struct {
	Subject struct {
		SubjectType string `json:"subject_type"`
		SubjectID   string `json:"subject_id"`
		Label       string `json:"label"`
	} `json:"subject"`
	Pages []struct {
		Slug  string   `json:"slug"`
		Name  string   `json:"name"`
		Paths []string `json:"paths"`
	} `json:"pages"`
	NextCursor string `json:"next_cursor"`
}

var pageAccessCmd = &cobra.Command{
	Use:   "access [slug]",
	Short: "Who reaches a page and by which paths, or which pages a subject reaches",
	Long: `Render a page's effective access, or one subject's.

  crewship page access fleet-201
  crewship page access --subject user:ada@example.com
  crewship page access --subject crew:lookout
  crewship page access --subject agent:watcher

A path is one of the API's own words:

  owner                 the page's owning user (or, for a crew, the owning crew)
  role                  the workspace role carries manage (ADMIN or OWNER)
  crew:<slug>           a member of the crew that owns the page
  panel_crew:<slug>     a member of a crew that owns a panel on it
  grant:page:<level>    a live read, produce or write grant
  panel_crew:withheld   a crew you cannot see; the server does not name it

This is a report, not a decision: the server derives every row from the
role, ownership, crew membership and live grants it already enforces, and
an inert grant is not a path. Reading a page's access takes the same right
as reading its grants — its owner, or a workspace admin. Asking about a
subject is an admin's, except that anyone may ask about themselves.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		subject := strings.TrimSpace(mustFlagString(cmd, "subject"))
		switch {
		case len(args) == 1 && subject != "":
			return cli.WithExitCode(errors.New(
				"a slug and --subject were both given; ask one question per command — who reaches the page, or what the subject reaches"),
				cli.ExitValidation)
		case len(args) == 0 && subject == "":
			return cli.WithExitCode(errors.New(
				"name a page (crewship page access <slug>) or a subject (--subject user:<email>, crew:<slug> or agent:<slug>)"),
				cli.ExitValidation)
		}
		if subject != "" {
			kind, _, found := strings.Cut(subject, ":")
			switch strings.ToLower(strings.TrimSpace(kind)) {
			case "user", "crew", "agent":
				if !found {
					return cli.WithExitCode(errors.New("--subject needs a reference after the kind: user:<email or id>, crew:<slug>, agent:<slug>"), cli.ExitValidation)
				}
			default:
				return cli.WithExitCode(fmt.Errorf(
					"--subject %q: the kind must be user, crew or agent, as in user:ada@example.com", subject), cli.ExitValidation)
			}
		}

		q := url.Values{}
		if limit, _ := cmd.Flags().GetInt("limit"); limit > 0 {
			q.Set("limit", fmt.Sprint(limit))
		}
		if cursor := strings.TrimSpace(mustFlagString(cmd, "cursor")); cursor != "" {
			q.Set("cursor", cursor)
		}

		client, err := pageClient()
		if err != nil {
			return err
		}
		var path string
		if subject != "" {
			q.Set("subject", subject)
			path = "/api/v1/pages/access?" + q.Encode()
		} else {
			path = "/api/v1/pages/" + pagePathEscape(args[0]) + "/access"
			if encoded := q.Encode(); encoded != "" {
				path += "?" + encoded
			}
		}
		resp, err := client.Get(path)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := pageCheckError(resp); err != nil {
			return err
		}
		raw, err := io.ReadAll(resp.Body)
		if err != nil {
			return fmt.Errorf("read response: %w", err)
		}
		f := newFormatter()
		switch f.Format {
		case "json", "yaml", "ndjson":
			return pageEmitMachine(f, raw, "{}")
		}

		if subject != "" {
			var doc pageSubjectAccessJSON
			if err := json.Unmarshal(raw, &doc); err != nil {
				return fmt.Errorf("decode response: %w", err)
			}
			if len(doc.Pages) == 0 {
				if f.Format != "quiet" {
					fmt.Printf("%s/%s reaches no page in this workspace.\n", doc.Subject.SubjectType, pageDash(doc.Subject.Label))
				}
				return nil
			}
			rows := make([][]string, 0, len(doc.Pages))
			for _, p := range doc.Pages {
				rows = append(rows, []string{p.Slug, pageDash(p.Name), strings.Join(p.Paths, ", ")})
			}
			f.Table([]string{"PAGE", "NAME", "PATHS"}, rows)
			pageAccessMoreHint(f.Format, doc.NextCursor)
			return nil
		}

		var doc pageAccessJSON
		if err := json.Unmarshal(raw, &doc); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
		if len(doc.Subjects) == 0 {
			if f.Format != "quiet" {
				fmt.Printf("Nobody reaches page %s.\n", args[0])
			}
			return nil
		}
		rows := make([][]string, 0, len(doc.Subjects))
		for _, s := range doc.Subjects {
			label := s.Label
			if label == "" {
				// The server withheld the name on purpose; printing the id
				// would print the word `withheld`, which is not a subject.
				label = "(withheld)"
			}
			rows = append(rows, []string{label, s.SubjectType, strings.Join(s.Paths, ", ")})
		}
		f.Table([]string{"SUBJECT", "KIND", "PATHS"}, rows)
		pageAccessMoreHint(f.Format, doc.NextCursor)
		return nil
	},
}

// pageAccessMoreHint says how to get the next page of a listing that did not
// end here. The cursor is opaque; the only thing to do with it is hand it
// back.
func pageAccessMoreHint(format, cursor string) {
	if cursor == "" || format == "quiet" {
		return
	}
	fmt.Printf("More rows follow: repeat with --cursor %s\n", cursor)
}

func init() {
	pageAccessCmd.Flags().String("subject", "",
		"Ask what this subject reaches instead of who reaches a page: user:<email or id>, crew:<slug> or agent:<slug>")
	pageAccessCmd.Flags().Int("limit", 0, "Rows per page (server default 100, at most 1000)")
	pageAccessCmd.Flags().String("cursor", "", "Continue a listing from the cursor a previous page printed")
	pageCmd.AddCommand(pageAccessCmd)
}
