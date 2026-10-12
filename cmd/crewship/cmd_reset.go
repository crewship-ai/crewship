//go:build !clionly

package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/config"
	"github.com/crewship-ai/crewship/internal/database"
	"github.com/crewship-ai/crewship/internal/provider/docker"
	"github.com/crewship-ai/crewship/internal/resourcelifecycle"
	"github.com/crewship-ai/crewship/internal/writerlease"
)

var resetDockerResources = docker.ResetOwnedResources

var resetCmd = &cobra.Command{
	Use: "reset", Short: "Reset application data of a stopped local installation",
	Long: `Reset a stopped local installation. Requires --data and an explicit absolute
--data-dir or CREWSHIP_HOME/CREWSHIP_DATA_DIR. Use --plan to review targets.

Only exact installation-labelled Docker resources are removed. Configuration,
secrets, installation identity, logs and backups are retained. Unlabelled Docker
resources are retained. Explicit stores outside the installation root are refused.
The database schema, migration metadata and nonce survive; application rows are
replaced with defaults from a fresh database of this version. No server is started.
Use stop -> reset --data -> start -> seed --server <url>.`,
	Args: cobra.NoArgs, RunE: runReset,
}

func init() {
	resetCmd.Flags().Bool("data", false, "Clear application data (required)")
	resetCmd.Flags().Bool("plan", false, "Print validated reset targets without changing files or Docker")
	resetCmd.Flags().String("data-dir", "", "Absolute installation root")
	resetCmd.Flags().String("config", "", "Server YAML config used by this installation")
	resetCmd.Flags().String("db", "", "Server database URL override")
}

type resetPlan struct {
	root, dbPath, boltPath string
	directories            []string
	cfg                    *config.Config
}

func prepareReset(cmd *cobra.Command) (*resetPlan, error) {
	data, _ := cmd.Flags().GetBool("data")
	if !data {
		return nil, fmt.Errorf("reset requires --data")
	}
	flagRoot, _ := cmd.Flags().GetString("data-dir")
	if strings.TrimSpace(flagRoot) == "" && strings.TrimSpace(os.Getenv("CREWSHIP_HOME")) == "" && strings.TrimSpace(os.Getenv("CREWSHIP_DATA_DIR")) == "" {
		return nil, fmt.Errorf("reset requires an explicit --data-dir, CREWSHIP_HOME or CREWSHIP_DATA_DIR; cwd and the user home cannot choose a destructive target")
	}
	if strings.TrimSpace(flagRoot) == "" {
		if legacy := strings.TrimSpace(os.Getenv("CREWSHIP_DATA_DIR")); legacy != "" && !filepath.IsAbs(legacy) {
			return nil, fmt.Errorf("reset requires absolute CREWSHIP_DATA_DIR; relative roots cannot select a destructive installation")
		}
	}
	root, err := database.ResolveDataDirRoot(flagRoot)
	if err != nil {
		return nil, err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return nil, fmt.Errorf("reset requires an existing installation root: %w", err)
	}
	if filepath.Dir(root) == root {
		return nil, fmt.Errorf("reset refuses filesystem root")
	}
	configPath, _ := cmd.Flags().GetString("config")
	if configPath != "" && !filepath.IsAbs(configPath) {
		return nil, fmt.Errorf("reset requires an absolute --config path; cwd cannot choose destructive configuration")
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		return nil, err
	}
	dbFlag, _ := cmd.Flags().GetString("db")
	if cfg.Container.Provider == "apple" {
		return nil, fmt.Errorf("offline reset does not yet support Apple container ownership cleanup")
	}
	paths, err := config.ResolvePaths(cfg, root, dbFlag)
	if err != nil {
		return nil, err
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	// Match the persistent path interpretation used by DatabaseLocation;
	// malformed file://authority URLs must not become cwd-relative paths.
	persistentPath := strings.TrimPrefix(strings.TrimPrefix(paths.DatabaseURL, "file://"), "file:")
	persistentPath, _, _ = strings.Cut(persistentPath, "?")
	if !filepath.IsAbs(persistentPath) {
		return nil, fmt.Errorf("reset requires an absolute SQLite database URL; relative DATABASE_URL cannot choose a destructive target")
	}
	if err := paths.CheckLegacyData(); err != nil {
		return nil, err
	}

	dbLocation, err := resourcelifecycle.DatabaseLocation(paths.DatabaseURL)
	if err != nil {
		return nil, err
	}
	if !strings.HasPrefix(dbLocation, "file:") {
		return nil, fmt.Errorf("reset requires a persistent local SQLite database")
	}
	plan := &resetPlan{root: root, dbPath: strings.TrimPrefix(dbLocation, "file:"), boltPath: cfg.State.BoltPath, cfg: cfg}
	directories := []string{cfg.Storage.BasePath, cfg.Storage.MemoryRoot, cfg.Storage.PageProjectsPath, filepath.Join(root, "chats"), filepath.Join(root, "skills")}
	reserved := []string{filepath.Join(root, "installations"), filepath.Join(root, "config"), filepath.Join(root, "logs"), cfg.Storage.LogPath, filepath.Join(root, "backups"), filepath.Join(root, "run"), plan.dbPath, filepath.Join(root, "secrets.env"), filepath.Join(root, "initial_setup_token"), filepath.Join(root, "cli-config.yaml")}
	if configPath != "" {
		absoluteConfig, err := filepath.Abs(configPath)
		if err != nil {
			return nil, err
		}
		reserved = append(reserved, absoluteConfig)
	}
	for _, path := range append(append([]string{}, directories...), plan.dbPath, plan.boltPath) {
		if path == "" {
			continue
		}
		canonical, err := resetCanonical(path)
		if err != nil {
			return nil, err
		}
		if !strictDescendant(root, canonical) {
			return nil, fmt.Errorf("reset refuses path outside or equal to installation root: %s", path)
		}
		if path != plan.dbPath && path != plan.boltPath {
			for _, keep := range reserved {
				if keep == "" {
					continue
				}
				keepCanonical, err := resetCanonical(keep)
				if err != nil {
					return nil, err
				}
				if canonical == keepCanonical || strictDescendant(canonical, keepCanonical) || strictDescendant(keepCanonical, canonical) {
					return nil, fmt.Errorf("reset store %s overlaps preserved path %s", path, keep)
				}
			}
			// Clearing a store may never remove the Bolt lock anchor either.
			boltCanonical, err := resetCanonical(plan.boltPath)
			if err != nil {
				return nil, err
			}
			if canonical == boltCanonical || strictDescendant(canonical, boltCanonical) || strictDescendant(boltCanonical, canonical) {
				return nil, fmt.Errorf("reset store overlaps Bolt state")
			}
			known := false
			for _, managed := range []string{"output", "data", "memory", "page-projects", "state/data", "state/memory", "state/page-projects", "chats", "skills"} {
				if canonical == filepath.Join(root, filepath.FromSlash(managed)) {
					known = true
					break
				}
			}
			if !known {
				return nil, fmt.Errorf("reset refuses an unproven custom store %s; migrate it to a managed installation path before reset", path)
			}
			plan.directories = append(plan.directories, canonical)
		}
	}
	boltCanonical, err := resetCanonical(plan.boltPath)
	if err != nil {
		return nil, err
	}
	plan.boltPath = boltCanonical

	for _, path := range []string{plan.dbPath, plan.boltPath} {
		for _, keptFile := range []string{"secrets.env", "initial_setup_token", "cli-config.yaml"} {
			canonicalKept, err := resetCanonical(filepath.Join(root, keptFile))
			if err != nil {
				return nil, err
			}
			if path == canonicalKept {
				return nil, fmt.Errorf("reset database/state overlaps preserved file %s", keptFile)
			}
		}
		if configPath != "" {
			absoluteConfig, err := filepath.Abs(configPath)
			if err != nil {
				return nil, err
			}
			canonicalConfig, err := resetCanonical(absoluteConfig)
			if err != nil {
				return nil, err
			}
			if path == canonicalConfig {
				return nil, fmt.Errorf("reset database/state overlaps server config")
			}
		}
		if err := resetSingleLink(path); err != nil {
			return nil, err
		}
		for _, keep := range []string{"installations", "run", "config", "logs", "backups"} {
			preserved, err := resetCanonical(filepath.Join(root, keep))
			if err != nil {
				return nil, err
			}
			if path == preserved || strictDescendant(preserved, path) {
				return nil, fmt.Errorf("reset database/state path %s overlaps preserved %s", path, keep)
			}
		}
	}
	if _, err := os.Stat(filepath.Join(root, ".git")); err == nil {
		return nil, fmt.Errorf("reset refuses an installation root that is a source checkout")
	} else if !os.IsNotExist(err) {
		return nil, err
	}

	if plan.dbPath == plan.boltPath {
		return nil, fmt.Errorf("SQLite and Bolt paths overlap")
	}
	for _, keep := range []string{"installations", "run", "config"} {
		resolved, err := resetCanonical(filepath.Join(root, keep))
		if err != nil {
			return nil, err
		}
		if !strictDescendant(root, resolved) {
			return nil, fmt.Errorf("reset refuses escaped %s directory", keep)
		}
	}
	if info, err := os.Stat(plan.dbPath); err != nil || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("reset requires an existing regular database file")
	}
	return plan, nil
}

func strictDescendant(root, path string) bool {
	relative, err := filepath.Rel(root, path)
	return err == nil && relative != "." && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}
func resetCanonical(path string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("reset requires absolute paths: %s", path)
	}
	tail := []string{}
	for candidate := filepath.Clean(path); ; candidate = filepath.Dir(candidate) {
		resolved, err := filepath.EvalSymlinks(candidate)
		if err == nil {
			for i := len(tail) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, tail[i])
			}
			return resolved, nil
		}
		if !os.IsNotExist(err) {
			return "", err
		}
		if info, e := os.Lstat(candidate); e == nil && info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("reset refuses dangling symlink: %s", candidate)
		}
		if parent := filepath.Dir(candidate); parent == candidate {
			return "", err
		}
		tail = append(tail, filepath.Base(candidate))
	}
}

func runReset(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()
	if err := ctx.Err(); err != nil {
		return err
	}
	plan, err := prepareReset(cmd)
	if err != nil {
		return err
	}
	preview, _ := cmd.Flags().GetBool("plan")
	if preview {
		formatter := resolvedFormatter(cmd)
		formatter.Writer = cmd.OutOrStdout()
		result := map[string]any{
			"installation":      plan.root,
			"database":          plan.dbPath,
			"bolt":              plan.boltPath,
			"clear_directories": plan.directories,
			"preserved":         []string{"configuration", "secrets", "identity", "logs", "backups"},
		}
		return formatter.AutoHuman(result, func() {
			fmt.Fprintf(cmd.OutOrStdout(), "Installation: %s\nSQLite rows: %s\nBolt contents: %s\n", plan.root, plan.dbPath, plan.boltPath)
			for _, path := range plan.directories {
				fmt.Fprintf(cmd.OutOrStdout(), "Clear contents: %s\n", path)
			}
			fmt.Fprintln(cmd.OutOrStdout(), "Preserve configuration, secrets, identity, logs and backups. Execution requires offline locks and exact Docker ownership.")
		})
	}
	operation, err := database.AcquireInstallationOperation(plan.root)
	if err != nil {
		return err
	}
	defer operation.Close()
	lease, err := writerlease.Acquire(plan.dbPath)
	if err != nil {
		return fmt.Errorf("stop the server before reset: %w", err)
	}
	defer lease.Close()
	db, err := database.Open("file:" + plan.dbPath)
	if err != nil {
		return err
	}
	defer db.Close()
	identity, err := resourcelifecycle.AcquireExistingIdentity(ctx, plan.root, db.DB, "file:"+plan.dbPath)
	if err != nil {
		return err
	}
	defer identity.Close()
	if _, err := os.Stat(plan.boltPath); err == nil {
		boltLease, err := writerlease.Acquire(plan.boltPath)
		if err != nil {
			return fmt.Errorf("Bolt state is in use: %w", err)
		}
		defer boltLease.Close()
	} else if !os.IsNotExist(err) {
		return err
	}
	freshDir, err := os.MkdirTemp(filepath.Join(plan.root, "run"), "reset-template-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(freshDir)
	freshPath := filepath.Join(freshDir, "crewship.db")
	fresh, err := database.Open("file:" + freshPath)
	if err != nil {
		return err
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err = database.Migrate(ctx, fresh.DB, logger); err == nil {
		err = database.RunPostDeployMigrations(ctx, fresh.DB, logger)
	}
	closeErr := fresh.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err := database.ValidateResetTemplate(ctx, db.DB, freshPath); err != nil {
		return err
	}
	if err := operation.Verify(); err != nil {
		return err
	}
	if err := lease.Verify(); err != nil {
		return err
	}
	if err := database.BeginOfflineReset(plan.root, identity.ID); err != nil {
		return err
	}
	if plan.cfg.Container.Provider != "apple" {
		if err := resetDockerResources(ctx, identity.ID); err != nil {
			return err
		}
	} else {
		return fmt.Errorf("offline reset does not yet support Apple container ownership cleanup")
	}
	// Revalidate after Docker inventory before deleting any host contents.
	verified, err := prepareReset(cmd)
	if err != nil {
		return err
	}
	if verified.root != plan.root || verified.dbPath != plan.dbPath || verified.boltPath != plan.boltPath || strings.Join(verified.directories, "\x00") != strings.Join(plan.directories, "\x00") {
		return fmt.Errorf("reset configuration changed during operation; retry while stopped")
	}
	if err := operation.Verify(); err != nil {
		return err
	}
	if err := lease.Verify(); err != nil {
		return err
	}
	confined, err := os.OpenRoot(plan.root)
	if err != nil {
		return err
	}
	defer confined.Close()
	for _, path := range plan.directories {
		if err := operation.Verify(); err != nil {
			return err
		}
		if err := lease.Verify(); err != nil {
			return err
		}
		relative, _ := filepath.Rel(plan.root, path)
		directory, err := confined.Open(relative)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		entries, err := directory.ReadDir(-1)
		closeErr := directory.Close()
		if err == nil {
			err = closeErr
		}
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if err := operation.Verify(); err != nil {
				return err
			}
			if err := lease.Verify(); err != nil {
				return err
			}
			if err := confined.RemoveAll(filepath.Join(relative, entry.Name())); err != nil {
				return err
			}
		}
		// Commit deletions before removing the durable recovery marker.
		directory, err = confined.Open(relative)
		if err != nil {
			return err
		}
		err = resetSyncDirectory(directory)
		closeErr = directory.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
	}
	if err := operation.Verify(); err != nil {
		return err
	}
	if err := lease.Verify(); err != nil {
		return err
	}
	if _, err := os.Stat(plan.boltPath); err == nil {
		relative, _ := filepath.Rel(plan.root, plan.boltPath)
		file, err := confined.OpenFile(relative, os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		err = file.Truncate(0)
		if err == nil {
			err = file.Sync()
		}
		closeErr := file.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := lease.Verify(); err != nil {
		return err
	}
	if err := operation.Verify(); err != nil {
		return err
	}
	if err := database.ResetApplicationData(ctx, db.DB, freshPath); err != nil {
		return err
	}
	if err := database.FinishOfflineReset(plan.root); err != nil {
		return err
	}
	fmt.Fprintln(cmd.ErrOrStderr(), "Application data reset; identity, configuration, secrets, logs and backups retained. Start the server, then seed its explicit URL.")
	return nil
}

// Keep the injected cleanup contract explicit in tests.
var _ func(context.Context, string) error = resetDockerResources
