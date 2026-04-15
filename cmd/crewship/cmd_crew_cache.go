package main

import (
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
)

// cacheImage mirrors the server-side CacheImageInfo response shape.
type cacheImage struct {
	Tag          string   `json:"tag"`
	Size         int64    `json:"size"`
	CreatedAt    int64    `json:"created_at"`
	ReferencedBy []string `json:"referenced_by"`
}

var crewCacheCmd = &cobra.Command{
	Use:   "cache",
	Short: "Manage devcontainer image cache (crewship-cache:*)",
}

var crewCacheListCmd = &cobra.Command{
	Use:   "list",
	Short: "List cached devcontainer images",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		client := newAPIClient()
		imgs, err := fetchCacheImages(client)
		if err != nil {
			return err
		}

		f := newFormatter()
		headers := []string{"TAG", "SIZE", "CREATED", "USED BY"}
		rows := make([][]string, 0, len(imgs))
		for _, img := range imgs {
			used := strings.Join(img.ReferencedBy, ", ")
			if used == "" {
				used = "—"
			}
			rows = append(rows, []string{
				img.Tag,
				formatSize(img.Size),
				formatAge(img.CreatedAt),
				used,
			})
		}
		return f.Auto(imgs, headers, rows)
	},
}

var (
	cachePruneOlderThan string
	cachePruneUnused    bool
	cachePruneForce     bool
)

var crewCachePruneCmd = &cobra.Command{
	Use:   "prune",
	Short: "Remove old or unreferenced cached images",
	Long: `Remove cached devcontainer images that are older than the given duration
and/or not referenced by any live crew.

With no flags, defaults to --older-than 30d (and leaves referenced images alone).
Always refuses to delete an image currently referenced by a crew's cached_image
column, unless --force is passed.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		// Defaulting: if neither flag set, treat as --older-than=30d.
		olderThan := cachePruneOlderThan
		if olderThan == "" && !cachePruneUnused {
			olderThan = "30d"
		}

		var olderThanDur time.Duration
		if olderThan != "" {
			d, err := parseDurationExtended(olderThan)
			if err != nil {
				return fmt.Errorf("invalid --older-than: %w", err)
			}
			olderThanDur = d
		}

		client := newAPIClient()
		imgs, err := fetchCacheImages(client)
		if err != nil {
			return err
		}

		now := time.Now()
		var targets []cacheImage
		for _, img := range imgs {
			// Never touch images a live crew still references, regardless of age.
			if len(img.ReferencedBy) > 0 {
				continue
			}
			ageOK := true
			if olderThanDur > 0 {
				created := time.Unix(img.CreatedAt, 0)
				ageOK = now.Sub(created) >= olderThanDur
			}
			if !ageOK {
				continue
			}
			// --unused by itself is implied by the ReferencedBy == 0 filter above.
			targets = append(targets, img)
		}

		if len(targets) == 0 {
			cli.PrintSuccess("No cache images match the prune criteria.")
			return nil
		}

		// Confirm interactively unless --force.
		if !cachePruneForce {
			fmt.Printf("Will remove %d cache image(s):\n", len(targets))
			for _, t := range targets {
				fmt.Printf("  %s (%s, age %s)\n", t.Tag, formatSize(t.Size), formatAge(t.CreatedAt))
			}
			fmt.Print("Continue? [y/N]: ")
			var resp string
			_, _ = fmt.Scanln(&resp)
			resp = strings.ToLower(strings.TrimSpace(resp))
			if resp != "y" && resp != "yes" {
				cli.PrintWarning("Aborted.")
				return nil
			}
		}

		removed := 0
		for _, t := range targets {
			if err := deleteCacheImage(client, t.Tag, false); err != nil {
				cli.PrintError(fmt.Sprintf("remove %s: %v", t.Tag, err))
				continue
			}
			removed++
		}
		cli.PrintSuccess(fmt.Sprintf("Removed %d of %d cache image(s).", removed, len(targets)))
		return nil
	},
}

func init() {
	crewCachePruneCmd.Flags().StringVar(&cachePruneOlderThan, "older-than", "",
		"Remove images older than this duration (e.g. 30d, 72h). Default: 30d when no flags set.")
	crewCachePruneCmd.Flags().BoolVar(&cachePruneUnused, "unused", false,
		"Remove only images not referenced by any crew (default behavior already skips referenced images).")
	crewCachePruneCmd.Flags().BoolVar(&cachePruneForce, "force", false,
		"Skip interactive confirmation.")

	crewCacheCmd.AddCommand(crewCacheListCmd)
	crewCacheCmd.AddCommand(crewCachePruneCmd)
	crewCmd.AddCommand(crewCacheCmd)
}

// fetchCacheImages calls GET /api/v1/cache/images and decodes the response.
func fetchCacheImages(client *cli.Client) ([]cacheImage, error) {
	resp, err := client.Get("/api/v1/cache/images")
	if err != nil {
		return nil, err
	}
	if err := cli.CheckError(resp); err != nil {
		return nil, err
	}
	var body struct {
		Images []cacheImage `json:"images"`
	}
	if err := cli.ReadJSON(resp, &body); err != nil {
		return nil, err
	}
	// Stable ordering for display.
	sort.Slice(body.Images, func(i, j int) bool {
		return body.Images[i].Tag < body.Images[j].Tag
	})
	return body.Images, nil
}

// deleteCacheImage calls DELETE /api/v1/cache/images/{tag}.
func deleteCacheImage(client *cli.Client, tag string, force bool) error {
	path := "/api/v1/cache/images/" + url.PathEscape(tag)
	if force {
		path += "?force=true"
	}
	resp, err := client.Delete(path)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return cli.CheckError(resp)
}

// parseDurationExtended parses Go's time.Duration format plus a "d" suffix
// for days. "30d" → 30*24h, "72h" → 72h, "15m" → 15m.
func parseDurationExtended(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty duration")
	}
	if strings.HasSuffix(s, "d") {
		var days int
		if _, err := fmt.Sscanf(s, "%dd", &days); err != nil {
			return 0, fmt.Errorf("parse %q: %w", s, err)
		}
		if days < 0 {
			return 0, fmt.Errorf("negative duration: %q", s)
		}
		return time.Duration(days) * 24 * time.Hour, nil
	}
	return time.ParseDuration(s)
}

// formatSize renders a byte count as a short human string (KB, MB, GB).
func formatSize(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(bytes)/float64(div), "KMGTPE"[exp])
}

// formatAge renders a Unix timestamp as a short relative age (e.g. "5d", "3h").
func formatAge(unixSec int64) string {
	if unixSec == 0 {
		return "—"
	}
	d := time.Since(time.Unix(unixSec, 0))
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}
