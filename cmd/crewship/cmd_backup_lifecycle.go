package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/crewship-ai/crewship/internal/backup"
	"github.com/crewship-ai/crewship/internal/cli"
)

var backupCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a new backup bundle",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		scope, _ := cmd.Flags().GetString("scope")
		crewRef, _ := cmd.Flags().GetString("crew")
		noEncrypt, _ := cmd.Flags().GetBool("no-encrypt")
		passphraseFile, _ := cmd.Flags().GetString("passphrase-file")
		recipient, _ := cmd.Flags().GetString("recipient")
		useKeyring, _ := cmd.Flags().GetBool("use-keyring")

		if scope != "workspace" && scope != "crew" {
			return fmt.Errorf("--scope must be 'workspace' or 'crew' (got %q)", scope)
		}
		if scope == "crew" && crewRef == "" {
			return fmt.Errorf("--crew <slug-or-id> is required when --scope=crew")
		}

		// Mutually-exclusive encryption selectors. --recipient overrides
		// --passphrase-file; --no-encrypt wins over both and skips the
		// prompt entirely.
		if recipient != "" && noEncrypt {
			return fmt.Errorf("--recipient and --no-encrypt are mutually exclusive")
		}
		if recipient != "" && passphraseFile != "" {
			return fmt.Errorf("--recipient and --passphrase-file are mutually exclusive")
		}

		var passphrase string
		switch {
		case noEncrypt:
			cli.PrintWarning("--no-encrypt: bundle will contain plaintext data. Protect it accordingly.")
		case recipient != "":
			if !strings.HasPrefix(recipient, "age1") {
				return fmt.Errorf("--recipient must be an age1… public key")
			}
			// Recipient is packed into the request body below as its
			// own JSON field; leave passphrase empty.
		default:
			// Keyring lookup short-circuits the prompt when the admin
			// asked for --use-keyring and we have a stored passphrase
			// for this workspace. Wrong keyring content surfaces as a
			// decryption failure during restore — the bundle itself is
			// still written with whatever passphrase the keyring held.
			ws := cli.ResolveWorkspace(flagWorkspace, cliCfg)
			// --use-keyring is an explicit user opt-in; surface
			// init/decrypt/write failures instead of silently degrading
			// to a prompt. The one error we DO swallow is
			// ErrKeyringEntryNotFound — that's the "first use on this
			// workspace" path where a fresh prompt is the correct
			// behaviour.
			var fromKeyring bool
			if useKeyring && passphraseFile == "" {
				kr, err := backup.DefaultKeyring(cmd.Context())
				if err != nil {
					return fmt.Errorf("open backup keyring: %w", err)
				}
				p, err := kr.GetPassphrase(cmd.Context(), ws)
				switch {
				case err == nil:
					passphrase = p
					fromKeyring = true
				case errors.Is(err, backup.ErrKeyringEntryNotFound):
					// fall through to the prompt below
				default:
					return fmt.Errorf("read backup keyring: %w", err)
				}
			}
			if passphrase == "" {
				p, err := readPassphrase(passphraseFile, true /*confirm*/)
				if err != nil {
					return err
				}
				passphrase = p
			}
			// Only persist AFTER the user confirmed a fresh prompt —
			// fromKeyring suppresses the re-write when the passphrase
			// came straight out of the keyring (re-encrypting the same
			// value just burns entropy and churns the file).
			// Store failures are reported as warnings rather than
			// aborting: the bundle is still going to be written, and
			// losing the keyring cache is recoverable at next use.
			if useKeyring && passphraseFile == "" && ws != "" && !fromKeyring {
				kr, err := backup.DefaultKeyring(cmd.Context())
				if err != nil {
					cli.PrintWarning(fmt.Sprintf("Keyring unavailable: %v", err))
				} else if err := kr.StorePassphrase(cmd.Context(), ws, passphrase); err != nil {
					cli.PrintWarning(fmt.Sprintf("Failed to store passphrase in keyring: %v", err))
				}
			}
		}

		// Resolve crew slug → ID if necessary.
		client := newAPIClient()
		var crewID string
		if scope == "crew" {
			var err error
			crewID, err = resolveCrewID(client, crewRef)
			if err != nil {
				return err
			}
		}

		outputDir, _ := cmd.Flags().GetString("output")
		body := map[string]any{
			"scope":      scope,
			"crew_id":    crewID,
			"passphrase": passphrase,
			"recipient":  recipient,
			"no_encrypt": noEncrypt,
			"output_dir": outputDir,
		}
		// Transport-security pre-flight before the encryption passphrase
		// rides the wire — mirrors cmd_login.go / cmd_setup.go. Blocks on
		// a structurally broken --server, warns on plaintext HTTP to a
		// non-loopback host. Skipped when nothing secret is being sent
		// (--no-encrypt / --recipient leave passphrase empty), so an
		// unencrypted backup over a plain-HTTP dev box doesn't get a
		// spurious credentials-in-the-clear warning. EffectiveServer (not
		// ResolveServer) so this matches what the client.Post below (via
		// newAPIClient) actually dials — flag > profile > env > config >
		// default (#1146/#1163).
		if passphrase != "" {
			if err := preflightServerURL(cmd.ErrOrStderr(), cli.EffectiveServer(flagServer, flagProfile, cliCfg)); err != nil {
				return err
			}
		}
		resp, err := client.Post("/api/v1/admin/backups", body)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var out struct {
			Path          string `json:"path"`
			Size          int64  `json:"size_bytes"`
			SHA256        string `json:"payload_sha256"`
			FormatVersion int    `json:"format_version"`
			Scope         string `json:"scope"`
			Encrypted     bool   `json:"encrypted"`
		}
		if err := cli.ReadJSON(resp, &out); err != nil {
			return err
		}
		cli.PrintSuccess(fmt.Sprintf("Backup created: %s", out.Path))
		f := newFormatter()
		headers := []string{"SCOPE", "SIZE", "ENCRYPTED", "FORMAT", "SHA256"}
		rows := [][]string{{
			out.Scope,
			formatBytes(out.Size),
			yesNo(out.Encrypted),
			fmt.Sprintf("v%d", out.FormatVersion),
			truncateLong(out.SHA256, 20),
		}}
		f.Table(headers, rows)
		return nil
	},
}

// restoreClamp mirrors backup.SecurityLevelClamp on the wire: one
// credential whose security_level the restore had to rewrite because the
// bundle carried a tier that does not exist (#1603).
type restoreClamp struct {
	CredentialID string `json:"credential_id"`
	Name         string `json:"name"`
	From         string `json:"from"`
	To           int    `json:"to"`
}

// droppedCol mirrors backup.DroppedColumn on the wire: one column the
// bundle carried that the target schema does not have, and which the
// restore therefore discarded (#2034).
type droppedCol struct {
	Table  string `json:"table"`
	Column string `json:"column"`
	Rows   int    `json:"rows"`
}

// rowCountMismatch mirrors backup.TableRowCountMismatch on the wire (#2009):
// one table whose row count did not match what the manifest recorded, at
// either the payload level (bundle vs. its own manifest) or the insert
// level (what landed on the target vs. the manifest).
type rowCountMismatch struct {
	Table    string `json:"table"`
	Recorded int    `json:"recorded"`
	Actual   int    `json:"actual"`
}

var backupRestoreCmd = &cobra.Command{
	Use:   "restore <file>",
	Short: "Restore a workspace or crew from a backup bundle",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		asWorkspace, _ := cmd.Flags().GetString("as-workspace")
		asCrew, _ := cmd.Flags().GetString("as-crew")
		passphraseFile, _ := cmd.Flags().GetString("passphrase-file")
		useKeyring, _ := cmd.Flags().GetBool("use-keyring")

		// In a non-interactive environment without --passphrase-file we
		// let the caller through with an empty passphrase so unencrypted
		// bundles restore from CI / scripts. The server will surface a
		// 400 if the bundle turns out to be encrypted and no passphrase
		// was supplied — cleaner than "no passphrase on stdin" from us.
		var passphrase string
		ws := cli.ResolveWorkspace(flagWorkspace, cliCfg)
		// Mirror the error-propagation policy used during create: the
		// only silent fallback is ErrKeyringEntryNotFound; every other
		// failure aborts so the admin sees the real cause instead of a
		// later "decryption failed" that's hard to diagnose.
		if useKeyring && passphraseFile == "" && ws != "" {
			kr, err := backup.DefaultKeyring(cmd.Context())
			if err != nil {
				return fmt.Errorf("open backup keyring: %w", err)
			}
			p, err := kr.GetPassphrase(cmd.Context(), ws)
			switch {
			case err == nil:
				passphrase = p
			case errors.Is(err, backup.ErrKeyringEntryNotFound):
				// fall through to prompt / stdin
			default:
				return fmt.Errorf("read backup keyring: %w", err)
			}
		}
		if passphrase == "" {
			if passphraseFile == "" && !term.IsTerminal(int(os.Stdin.Fd())) {
				passphrase = ""
			} else {
				p, err := readPassphrase(passphraseFile, false /*no confirm*/)
				if err != nil {
					return err
				}
				passphrase = p
			}
		}

		dryRun, _ := cmd.Flags().GetBool("dry-run")
		replace, _ := cmd.Flags().GetBool("replace")
		filesOnly, _ := cmd.Flags().GetBool("files-only")
		if filesOnly && (asWorkspace != "" || asCrew != "" || replace) {
			return fmt.Errorf("--files-only cannot be combined with --as-workspace, --as-crew or --replace: it lands container state into crews that already exist")
		}
		body := map[string]any{
			"path":         args[0],
			"passphrase":   passphrase,
			"as_workspace": asWorkspace,
			"as_crew":      asCrew,
			"replace":      replace,
			"dry_run":      dryRun,
			"files_only":   filesOnly,
		}
		client := newAPIClient()
		resp, err := client.Post("/api/v1/admin/backups/restore", body)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var out struct {
			RestoredWs             string         `json:"restored_ws"`
			RestoredWorkspaceID    string         `json:"restored_workspace_id"`
			CrewsCount             int            `json:"crews_count"`
			CrewsRestored          int            `json:"crews_restored"`
			RowsInserted           int            `json:"rows_inserted"`
			DockerPhaseSkipped     bool           `json:"docker_phase_skipped"`
			DroppedCrewFilesystems []string       `json:"dropped_crew_filesystems"`
			SecurityLevelClamped   int            `json:"security_level_clamped"`
			SecurityLevelClamps    []restoreClamp `json:"security_level_clamps"`
			ColumnsDropped         int            `json:"columns_dropped"`
			DroppedColumns         []droppedCol   `json:"dropped_columns"`
			// #2009: does the decrypted payload match what the manifest
			// recorded, and did the insert land what the payload carries.
			PayloadRowCountMismatches []rowCountMismatch `json:"payload_row_count_mismatches"`
			RowsInsertedShortfalls    []rowCountMismatch `json:"rows_inserted_shortfalls"`
			// #2226: a forked restore regenerates the ids the journal
			// hash chain commits to, so the chain is re-signed at a new
			// genesis. Zero on a plain restore.
			JournalEntriesResigned     int `json:"journal_entries_resigned"`
			JournalCheckpointsResigned int `json:"journal_checkpoints_resigned"`
		}
		if err := cli.ReadJSON(resp, &out); err != nil {
			return err
		}
		prefix := "Restore complete"
		if dryRun {
			// The admin asked for a verify-only run. No workspace /
			// crew / agent rows changed and the docker phase was
			// skipped; the only side effect is one
			// backup.restore.dry_run row in the audit log so an
			// auditor can see who tested what.
			prefix = "Restore validation complete (dry-run; no workspace/crew data changes applied)"
		}
		msg := fmt.Sprintf(
			"%s — workspace=%s crews=%d rows=%d",
			prefix, out.RestoredWs, out.CrewsCount, out.RowsInserted,
		)
		if out.RestoredWorkspaceID != "" {
			msg += " id=" + out.RestoredWorkspaceID
		}
		cli.PrintSuccess(msg)
		// The docker-phase warning only matters on a real restore —
		// dry-run never touches docker, so surfacing "you still need
		// to provision crews" would mislead the admin into thinking
		// the DB mutated when it did not.
		//
		// The old text told the admin to "re-run restore without the
		// rewrite flag", which the server rejects 100% of the time — the
		// forked workspace can never match the bundle's id or slug
		// (#1716). What lands the files is --files-only, authorised by
		// the provenance this very restore just recorded. Naming the
		// crews matters too: the server has always known which ones lost
		// their filesystem data and never told anyone.
		if !dryRun && out.DockerPhaseSkipped {
			target := out.RestoredWorkspaceID
			if target == "" {
				target = "<workspace>"
			}
			warning := "Docker phase skipped (--as-workspace/--as-crew supplied) — crew files are NOT restored yet."
			if len(out.DroppedCrewFilesystems) > 0 {
				warning += fmt.Sprintf("\n  Crews still missing their container state: %s.", strings.Join(out.DroppedCrewFilesystems, ", "))
			}
			// Command names come from internal/backup so this copy — the
			// one the operator actually reads — cannot drift from the
			// server's. It said `crew provision` for a while after the
			// server stopped: provision builds an image, and --files-only
			// writes by exec'ing into a container that has to be running.
			steps := backup.ForkedRestoreSteps("<crew>", target, args[0])
			warning += fmt.Sprintf("\n  Finish the restore with:\n    %s\n    %s", steps[0], steps[1])
			cli.PrintWarning(warning)
		}
		// A re-signed chain is not a warning — the restore did the right
		// thing — but it IS a change of meaning the operator has to be
		// handed at the moment it happens: the fork's journal verifies
		// clean while attesting to THIS instance only, with no
		// cryptographic link back to the source's history. Saying nothing
		// would let a later clean `journal verify` be read as provenance
		// the fork does not have.
		//
		// Printed on a dry run too, and unlike the docker warning it
		// belongs there: "this fork would start a new chain" is exactly
		// what an operator wants to hear BEFORE cutover, not after.
		if out.JournalEntriesResigned > 0 {
			verb, tense := "re-signed", "starts"
			if dryRun {
				verb, tense = "would be re-signed", "would start"
			}
			note := fmt.Sprintf("Journal chain %s: %d entries", verb, out.JournalEntriesResigned)
			if out.JournalCheckpointsResigned > 0 {
				note += fmt.Sprintf(", %d compaction checkpoints", out.JournalCheckpointsResigned)
			}
			note += fmt.Sprintf(".\n  The fork %s a NEW chain under this instance's key — it no longer links back to the source workspace.", tense)
			note += "\n  Recorded in the fork's own journal as a `backup.chain_resigned` entry."
			cli.PrintSuccess(note)
		}
		// CrewsRestored, not CrewsCount: the first is what landed, the
		// second is what the bundle describes. Printing the bundle's
		// number here would report a resume that wrote to nothing as a
		// success — the same shape of claim the manifest used to make
		// about memory it did not contain.
		if !dryRun && filesOnly {
			cli.PrintSuccess(fmt.Sprintf("Container state landed for %d of %d crew(s) in the bundle; no database rows were changed.",
				out.CrewsRestored, out.CrewsCount))
		}
		// Unlike the docker warning this one DOES matter on a dry run:
		// "this bundle carries credentials at a tier that does not
		// exist" is exactly what an admin wants to hear before they
		// commit to the restore, not after.
		if out.SecurityLevelClamped > 0 {
			verb := "were clamped to"
			if dryRun {
				verb = "would be clamped to"
			}
			details := make([]string, 0, len(out.SecurityLevelClamps))
			for _, c := range out.SecurityLevelClamps {
				label := c.CredentialID
				if c.Name != "" {
					label = c.Name
				}
				details = append(details, fmt.Sprintf("%s (bundle said %s)", label, c.From))
			}
			more := ""
			if out.SecurityLevelClamped > len(details) {
				more = fmt.Sprintf(" (+%d more)", out.SecurityLevelClamped-len(details))
			}
			cli.PrintWarning(fmt.Sprintf(
				"%d credential(s) carried a security_level outside L1-L4 and %s the strictest tier: %s%s. Re-set each one with `crewship credential update <name> --security-level N`.",
				out.SecurityLevelClamped, verb, strings.Join(details, ", "), more))
		}
		// Also a dry-run-relevant warning, and for a stronger reason than
		// the clamp: a clamped credential is still restored, whereas a row
		// that needed a dropped column to satisfy a NOT NULL or a primary
		// key was not restored at all and nothing else in this output says
		// so. `rows=` counts what landed, not what was meant to (#2034).
		if out.ColumnsDropped > 0 {
			verb := "were dropped"
			if dryRun {
				verb = "would be dropped"
			}
			details := make([]string, 0, len(out.DroppedColumns))
			counted := 0
			for _, d := range out.DroppedColumns {
				counted += d.Rows
				details = append(details, fmt.Sprintf("%s.%s (%d row(s))", d.Table, d.Column, d.Rows))
			}
			more := ""
			if out.ColumnsDropped > counted {
				more = fmt.Sprintf(" (+%d more)", out.ColumnsDropped-counted)
			}
			cli.PrintWarning(fmt.Sprintf(
				"%d value(s) in this bundle %s because this instance's schema has no such column: %s%s.\n"+
					"  The bundle was written against a different schema. Rows that needed one of those columns to satisfy a NOT NULL or a primary key did NOT land, and the restore could not report them individually.\n"+
					"  Check the tables named above before treating this restore as complete.",
				out.ColumnsDropped, verb, strings.Join(details, ", "), more))
		}
		// #2009: does the decrypted payload actually carry what the
		// manifest claimed at create time. Distinct from columns_dropped —
		// this is the bundle disagreeing with its OWN manifest, not with
		// the target schema.
		if len(out.PayloadRowCountMismatches) > 0 {
			details := make([]string, 0, len(out.PayloadRowCountMismatches))
			for _, m := range out.PayloadRowCountMismatches {
				details = append(details, fmt.Sprintf("%s (recorded %d, actual %d)", m.Table, m.Recorded, m.Actual))
			}
			cli.PrintWarning(fmt.Sprintf(
				"This bundle's payload does not match its own manifest for %d table(s): %s.\n"+
					"  The manifest was written against a different dump than the payload carries — treat this bundle as suspect before relying on it for disaster recovery.",
				len(out.PayloadRowCountMismatches), strings.Join(details, "; ")))
		}
		// #2009: did the insert pass actually land what the payload
		// carries, table by table — the summary that catches a shortfall
		// none of the more specific reports above (columns_dropped, a PK
		// collision) named. Never printed on a dry run — nothing was
		// inserted to compare.
		if len(out.RowsInsertedShortfalls) > 0 {
			details := make([]string, 0, len(out.RowsInsertedShortfalls))
			for _, m := range out.RowsInsertedShortfalls {
				details = append(details, fmt.Sprintf("%s (recorded %d, landed %d)", m.Table, m.Recorded, m.Actual))
			}
			cli.PrintWarning(fmt.Sprintf(
				"Fewer rows landed than the manifest recorded for %d table(s): %s.\n"+
					"  Check ColumnsDropped and the tables above for a specific cause; if none apply, a primary-key collision on the target likely swallowed them.",
				len(out.RowsInsertedShortfalls), strings.Join(details, "; ")))
		}
		return nil
	},
}

var backupDeleteCmd = &cobra.Command{
	Use:   "delete <file>",
	Short: "Delete a backup bundle from disk",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		force := skipConfirm(cmd)
		// Interactive confirmation unless --yes/--force. Silent deletion
		// of a multi-GB bundle from a fat-fingered command is exactly the
		// kind of footgun we want to put behind a speed bump. When
		// stdin isn't a TTY (scripts, CI) we require pre-confirmation
		// instead of prompting.
		if !force {
			if !term.IsTerminal(int(os.Stdin.Fd())) {
				return fmt.Errorf("refusing to delete %s without --yes (or --force) in a non-interactive session", args[0])
			}
			fmt.Fprintf(os.Stderr, "Delete backup %s? [y/N] ", args[0])
			reader := bufio.NewReader(os.Stdin)
			line, _ := reader.ReadString('\n')
			line = strings.TrimSpace(strings.ToLower(line))
			if line != "y" && line != "yes" {
				fmt.Fprintln(os.Stderr, "Aborted.")
				return nil
			}
		}
		client := newAPIClient()
		resp, err := client.Delete("/api/v1/admin/backups?path=" + encodeQuery(args[0]))
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		resp.Body.Close()
		cli.PrintSuccess("Backup deleted: " + args[0])
		return nil
	},
}
