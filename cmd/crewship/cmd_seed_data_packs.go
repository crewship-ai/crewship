package main

// Demo pack seeding — the two things a pack needs beyond what the other
// phases already create (crews, agents, skills, routines, pages, issues):
//
//   - its deterministic scripts in the crew's shared volume, delivered
//     BEFORE provisioning so the host can still write the tree (after the
//     entrypoint chowns it to the agent UID the write has to go through a
//     running container, and a freshly seeded crew has none);
//   - a crew-scoped GitHub credential, so that `{{ secrets.CLI_TOKEN }}` in
//     the pack's routines resolves to the real token and not to whichever
//     CLI_TOKEN the demo vault created last. The resolver picks by TYPE,
//     preferring the author crew's own credential and otherwise the newest
//     (credential_resolver.go) — and the inert demo accounts are created
//     after the real one, so without crew scope they would win.

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"os"

	"github.com/crewship-ai/crewship/cmd/crewship/seeddata"
	"github.com/crewship-ai/crewship/internal/cli"
)

// packGitHubEnv is the seed variable that makes the GitHub-backed packs
// runnable. Read-only scopes are enough (actions:read + repo read): the
// packs never write to GitHub.
const packGitHubEnv = "SEED_GITHUB_TOKEN"

// packCredentialName is the name of the crew-scoped credential a pack
// receives. One per pack crew, so `crewship credential resolve <agent>`
// shows which pack an agent's GH_TOKEN comes from.
func packCredentialName(p seeddata.PackDef) string {
	return "github-" + p.Slug
}

// packBoundSlots records "crewSlug/SLOT" pairs the pack phase bound, so the
// demo vault's inert bindings (github-acme → GH_TOKEN on engineering, and so
// on) skip a crew whose slot already carries a real token instead of 409ing
// or — worse, if they ran first — shadowing it.
var packBoundSlots = map[string]bool{}

// seedPackFiles delivers every pack's scripts and config to its crew's shared
// volume. Non-fatal by file: a pack missing one script is reported and still
// leaves a workspace worth opening; `seed verify` catches the drift anyway by
// comparing the delivered bytes against the embedded ones.
func seedPackFiles(ctx context.Context, client *cli.Client, crewIDs map[string]string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "Delivering demo pack files...")
	saved, failed := 0, 0
	for _, p := range seeddata.Packs {
		crewID, ok := crewIDs[p.CrewSlug]
		if !ok {
			fmt.Fprintf(os.Stderr, "  ! pack %s: crew %q not seeded — %d file(s) skipped\n", p.Slug, p.CrewSlug, len(p.Files))
			failed += len(p.Files)
			continue
		}
		for _, f := range p.Files {
			if err := ctx.Err(); err != nil {
				return err
			}
			content, err := seeddata.PackFileContent(f.Src)
			if err != nil {
				fmt.Fprintf(os.Stderr, "  ! pack %s: %v\n", p.Slug, err)
				failed++
				continue
			}
			if err := putBytes(ctx, client, crewFileSavePath(crewID, f.Dest), bytes.NewReader(content)); err != nil {
				fmt.Fprintf(os.Stderr, "  ! pack %s: %s: %v\n", p.Slug, f.Dest, err)
				failed++
				continue
			}
			saved++
		}
		fmt.Fprintf(os.Stderr, "  + pack %s: %d file(s) → crew %s\n", p.Slug, len(p.Files), p.CrewSlug)
	}
	if failed > 0 {
		fmt.Fprintf(os.Stderr, "  %d file(s) delivered, %d failed\n", saved, failed)
	}
	return nil
}

// crewFileSavePath is the crew-files save route for one destination.
func crewFileSavePath(crewID, dest string) string {
	return "/api/v1/crews/" + crewID + "/files/save?path=" + dest
}

// seedPackCredentials creates the crew-scoped GitHub credential for every
// pack that needs one and binds it to the crew's GH_TOKEN slot. Without
// SEED_GITHUB_TOKEN it creates nothing and says so — an inert token that
// looked real would be exactly the silent failure the packs exist to catch.
func seedPackCredentials(ctx context.Context, client *cli.Client, crewIDs map[string]string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	token := os.Getenv(packGitHubEnv)
	for _, p := range seeddata.Packs {
		if !packRequires(p, packGitHubEnv) {
			continue
		}
		if token == "" {
			fmt.Fprintf(os.Stderr, "  = pack %s: %s not set — no crew credential created; `crewship seed verify` will report the pack as skipped\n",
				p.Slug, packGitHubEnv)
			continue
		}
		crewID, ok := crewIDs[p.CrewSlug]
		if !ok {
			fmt.Fprintf(os.Stderr, "  ! pack %s: crew %q not seeded — credential skipped\n", p.Slug, p.CrewSlug)
			continue
		}
		name := packCredentialName(p)
		credID, err := seedOneCrewCredential(client, seeddata.CredentialDef{
			Name:        name,
			Description: fmt.Sprintf("GitHub token (read-only) for the %s demo pack — scoped to crew %s so its routines resolve this one", p.Name, p.CrewSlug),
			Type:        "CLI_TOKEN",
			Provider:    "GITHUB",
			EnvVarName:  "GH_TOKEN",
			Value:       token,
		}, crewID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  ! pack %s credential: %v\n", p.Slug, err)
			continue
		}
		resp, err := client.Post("/api/v1/credentials/bindings", map[string]string{
			"credential_id": credID,
			"slot":          "GH_TOKEN",
			"scope":         "CREW",
			"crew_id":       crewID,
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "  ! pack %s binding: %v\n", p.Slug, err)
			continue
		}
		if resp.StatusCode != http.StatusConflict {
			if err := cli.CheckError(resp); err != nil {
				fmt.Fprintf(os.Stderr, "  ! pack %s binding: %v\n", p.Slug, err)
				resp.Body.Close()
				continue
			}
		}
		resp.Body.Close()
		packBoundSlots[p.CrewSlug+"/GH_TOKEN"] = true
		fmt.Fprintf(os.Stderr, "  + pack %s: %s → GH_TOKEN (crew %s)\n", p.Slug, name, p.CrewSlug)
	}
	return nil
}

// seedOneCrewCredential creates a CREW-scoped credential, resolving an
// existing one by name so a re-seed is idempotent.
func seedOneCrewCredential(client *cli.Client, def seeddata.CredentialDef, crewID string) (string, error) {
	if existing, err := resolveByName(client, "/api/v1/credentials", def.Name); err == nil && existing != "" {
		fmt.Fprintf(os.Stderr, "  = Credential exists: %s\n", def.Name)
		return existing, nil
	}
	resp, err := client.Post("/api/v1/credentials", map[string]interface{}{
		"name":        def.Name,
		"description": def.Description,
		"value":       def.Value,
		"type":        def.Type,
		"provider":    def.Provider,
		"scope":       "CREW",
		"crew_id":     crewID,
	})
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusConflict {
		if existing, rerr := resolveByName(client, "/api/v1/credentials", def.Name); rerr == nil && existing != "" {
			return existing, nil
		}
		return "", fmt.Errorf("%s: conflict but existing record could not be resolved", def.Name)
	}
	if err := cli.CheckError(resp); err != nil {
		return "", err
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := cli.ReadJSON(resp, &created); err != nil {
		return "", err
	}
	return created.ID, nil
}

// packRequires reports whether a pack lists env among its requirements.
func packRequires(p seeddata.PackDef, env string) bool {
	for _, k := range p.RequiresEnv {
		if k == env {
			return true
		}
	}
	return false
}

// packRunnable reports whether every requirement of a pack is met in the
// current environment — the gate the page producer phase and `seed verify`
// both use, so a pack is skipped for the same reason in both places.
func packRunnable(p seeddata.PackDef) (bool, string) {
	missing := seeddata.MissingPackEnv(p, os.Getenv)
	if missing == "" {
		return true, ""
	}
	return false, missing + " not set"
}
