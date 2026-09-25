package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/crewship-ai/crewship/internal/cli"
)

// seedStoryPageFolder gives the business story a home in Pages. It only files
// an unfiled page: a re-seed must not undo a person's later move.
func seedStoryPageFolder(ctx context.Context, client *cli.Client) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	const slug = "harbor-goods"
	resp, err := client.Get("/api/v1/page-folders/" + pagePathEscape(slug))
	if err != nil {
		return err
	}
	if resp.StatusCode == http.StatusNotFound {
		resp.Body.Close()
		resp, err = client.Post("/api/v1/page-folders", map[string]any{
			"slug": slug, "name": "Harbor Goods", "owner": "crew/ops", "icon": "inbox", "color": "blue",
		})
		if err != nil {
			return err
		}
	}
	if err := cli.CheckError(resp); err != nil {
		return fmt.Errorf("create or read folder: %w", err)
	}
	resp.Body.Close()
	page, err := pageFolderReadPage(client, "leads-at-risk")
	if err != nil {
		return err
	}
	if page.Folder != nil {
		return nil
	}
	folderRaw, err := pageFolderGet(client, "/api/v1/page-folders/"+pagePathEscape(slug))
	if err != nil {
		return err
	}
	var folder PageFolderJSON
	if err := json.Unmarshal(folderRaw, &folder); err != nil {
		return err
	}
	version := folder.ACLVersion
	move, err := client.Post("/api/v1/page-folders/"+pagePathEscape(slug)+"/pages", pageFolderMoveBody{
		Page: page.Slug, PagesVersion: page.PagesVersion, ACLVersion: &version,
	})
	if err != nil {
		return err
	}
	defer move.Body.Close()
	return cli.CheckError(move)
}
