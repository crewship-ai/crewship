package main

// POST /api/v1/pages/{slug}/document/presence doc presence

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
)

var docPresenceClient = regexp.MustCompile(`^[A-Za-z0-9_-]{8,64}$`)
var docPresenceCardID = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,80}$`)

// docPresenceCanvas builds the optional Canvas presence from --pointer X,Y and --select ID,ID.
func docPresenceCanvas(pointer, selection string) (map[string]any, error) {
	canvas := map[string]any{}
	if pointer != "" {
		parts := strings.Split(pointer, ",")
		if len(parts) != 2 {
			return nil, fmt.Errorf("--pointer must be X,Y in canvas coordinates")
		}
		point := map[string]any{}
		for i, key := range []string{"x", "y"} {
			value, err := strconv.ParseFloat(strings.TrimSpace(parts[i]), 64)
			if err != nil || math.IsNaN(value) || math.Abs(value) > 1_000_000 {
				return nil, fmt.Errorf("--pointer must be X,Y with each between -1000000 and 1000000")
			}
			point[key] = value
		}
		canvas["pointer"] = point
	}
	if selection != "" {
		ids := strings.Split(selection, ",")
		if len(ids) > 8 {
			return nil, fmt.Errorf("--select takes at most 8 card IDs")
		}
		seen := map[string]bool{}
		for _, id := range ids {
			if !docPresenceCardID.MatchString(id) {
				return nil, fmt.Errorf("--select card IDs are 1 to 80 letters, digits, - or _")
			}
			if seen[id] {
				return nil, fmt.Errorf("--select lists %s twice", id)
			}
			seen[id] = true
		}
		canvas["selection"] = ids
	}
	if len(canvas) == 0 {
		return nil, nil
	}
	return canvas, nil
}

func newDocPresenceCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "presence <page-slug>", Short: "Show who else has a document open (one presence heartbeat)", Long: `Send one presence heartbeat for this tab id and print the other people on the
document: their user id, the name the server resolved for them, their colour
index and their caret as encoded Yjs relative positions. Presence is ephemeral:
the server keeps it in memory for a few seconds and never stores it. Every
heartbeat is authorized like a document read, so a revoked reader is refused
(404) and removed. --leave removes this tab at once.

On a Canvas document, --pointer X,Y (canvas coordinates) and --select ID,ID
(at most 8 cards: Page block or canvas item IDs) share where this tab is on
the Canvas view; people on the Canvas show up with a "canvas" object.`, Example: `  crewship doc presence @<artifact-id>
  crewship doc presence @<artifact-id> --pointer 240,120 --select intro
  crewship doc presence @<artifact-id> --client my-script-tab --leave`, Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		id, _ := cmd.Flags().GetString("client")
		if id == "" {
			raw := make([]byte, 8)
			if _, err := rand.Read(raw); err != nil {
				return err
			}
			id = "cli-" + hex.EncodeToString(raw)
		}
		if !docPresenceClient.MatchString(id) {
			return fmt.Errorf("--client must be 8 to 64 letters, digits, - or _")
		}
		leave, _ := cmd.Flags().GetBool("leave")
		body := map[string]any{"client": id}
		if leave {
			body["leave"] = true
		}
		pointer, _ := cmd.Flags().GetString("pointer")
		selection, _ := cmd.Flags().GetString("select")
		canvas, err := docPresenceCanvas(pointer, selection)
		if err != nil {
			return err
		}
		if canvas != nil && leave {
			return fmt.Errorf("--leave removes this tab; it cannot also share --pointer or --select")
		}
		if canvas != nil {
			body["canvas"] = canvas
		}
		client, err := pageClient()
		if err != nil {
			return err
		}
		response, err := client.Post("/api/v1/pages/"+pagePathEscape(args[0])+"/document/presence", body)
		if err != nil {
			return err
		}
		return printPageArtifactResponse(cmd, response)
	}}
	cmd.Flags().String("client", "", "Tab id to heartbeat as (8-64 of A-Z a-z 0-9 - _); default: a new random id")
	cmd.Flags().Bool("leave", false, "Remove this tab from the document's presence")
	cmd.Flags().String("pointer", "", "Canvas pointer as X,Y in canvas coordinates")
	cmd.Flags().String("select", "", "Selected Canvas cards as comma-separated IDs (at most 8)")
	return cmd
}
