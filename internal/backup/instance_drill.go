package backup

// Proof level 3 — a test restore. `crewship backup drill` restores an
// instance bundle into a throwaway data directory with the same code as
// `crewship recover`, starts nothing (no server, no scheduler, no container,
// no network client exists in this path), and then checks the restored copy
// the way a real recovery would be judged: attachments open, memory loads,
// credentials unlock, the journal verifies, routines stay held.

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"filippo.io/age"

	"github.com/crewship-ai/crewship/internal/database"
	"github.com/crewship-ai/crewship/internal/encryption"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/quiesce"
)

// Drill check names and statuses.
const (
	DrillCheckAttachments = "attachments_open"
	DrillCheckMemory      = "memory_loads"
	DrillCheckCredentials = "credentials_unlock"
	DrillCheckJournal     = "journal_verifies"
	DrillCheckHeld        = "routines_held"
	DrillCheckServiceData = "service_data_staged"

	DrillStatusOK      = "ok"
	DrillStatusFailed  = "failed"
	DrillStatusSkipped = "skipped"
)

// DrillOptions configure DrillInstance.
type DrillOptions struct {
	BundlePath string
	Identities []age.Identity
	Passphrase string
	// UseEnvKeys unlocks credentials with this process's ENCRYPTION_KEY /
	// ENCRYPTION_KEY_V<N> when the bundle has no recovery kit.
	UseEnvKeys bool
	// TempRoot is where the throwaway data directory goes (default: the
	// system temp dir). It is always removed.
	TempRoot string
	Actor    string
}

// DrillCheck is one check's outcome.
type DrillCheck struct {
	Name    string `json:"name"`
	Status  string `json:"status"`
	Checked int    `json:"checked"`
	Failed  int    `json:"failed"`
	Detail  string `json:"detail"`
}

// DrillReport is the drill's result — what `--post` sends to the server.
type DrillReport struct {
	Result     string         `json:"result"`
	Summary    string         `json:"summary"`
	BundlePath string         `json:"bundle_path"`
	SHA256     string         `json:"sha256"`
	Isolation  string         `json:"isolation"`
	KeySource  string         `json:"key_source"`
	Checks     []DrillCheck   `json:"checks"`
	Restore    *RecoverReport `json:"restore,omitempty"`
	Error      string         `json:"error,omitempty"`
	StartedAt  time.Time      `json:"started_at"`
	FinishedAt time.Time      `json:"finished_at"`
}

// Failures lists every check that did not pass, with its detail — the "what
// failed" a partial result must carry.
func (r *DrillReport) Failures() []string {
	var out []string
	for _, c := range r.Checks {
		if c.Status != DrillStatusOK {
			out = append(out, fmt.Sprintf("%s: %s", c.Name, c.Detail))
		}
	}
	return out
}

// DrillInstance runs a test restore and its checks. The returned report is
// complete even when the result is failed; the error is reserved for
// problems before a restore could start (bad options, unreadable bundle).
func DrillInstance(ctx context.Context, opts DrillOptions) (*DrillReport, error) {
	rep := &DrillReport{
		Result: RestoreResultFailed, BundlePath: opts.BundlePath, StartedAt: time.Now().UTC(),
		Isolation: "throwaway data directory; no server, scheduler, container or network client started",
		Checks:    []DrillCheck{},
	}
	m, err := Inspect(ctx, opts.BundlePath)
	if err != nil {
		return nil, err
	}
	rep.SHA256 = m.Checksums.PayloadSHA256
	tmp, err := os.MkdirTemp(opts.TempRoot, "crewship-drill-")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	dataDir := filepath.Join(tmp, "data")

	restore, err := RecoverInstance(ctx, RecoverOptions{
		BundlePath: opts.BundlePath, Identities: opts.Identities, Passphrase: opts.Passphrase,
		DataDir: dataDir, Drill: true, Actor: opts.Actor,
	})
	rep.Restore = restore
	if err != nil {
		rep.Error = err.Error()
		rep.Summary = "the test restore failed: " + err.Error()
		rep.FinishedAt = time.Now().UTC()
		return rep, nil
	}

	keys, source, chainSeed := drillKeys(restore.Kit(), opts.UseEnvKeys)
	rep.KeySource = source
	db, err := database.Open("file:" + restore.DatabasePath)
	if err != nil {
		rep.Error = err.Error()
		rep.Summary = "the restored database does not open: " + err.Error()
		rep.FinishedAt = time.Now().UTC()
		return rep, nil
	}
	defer func() { _ = db.Close() }()

	rep.Checks = append(rep.Checks,
		drillAttachments(ctx, db.DB, RecoveredStoreDir(dataDir, StoreOutput)),
		drillMemory(ctx, db.DB),
		drillCredentials(ctx, db.DB, keys, source),
		drillJournal(ctx, db.DB, chainSeed),
		drillHeld(ctx, db.DB),
	)
	if restore.ServiceSnapshots > 0 {
		checked, err := LandRecoveredServices(ctx, db.DB, dataDir, nil, ServiceLandingOptions{DryRun: true})
		c := DrillCheck{Name: DrillCheckServiceData, Checked: checked, Status: DrillStatusOK, Detail: "disk images, ownership, fresh generations and maintenance verified; physical helper import is a separate host check"}
		if err != nil {
			c.Status = DrillStatusFailed
			c.Failed = 1
			c.Detail = err.Error()
		}
		rep.Checks = append(rep.Checks, c)
	}
	rep.Result = RestoreResultOK
	for _, c := range rep.Checks {
		if c.Status != DrillStatusOK {
			rep.Result = RestoreResultPartial
		}
	}
	if restore.Result == RestoreResultPartial {
		rep.Result = RestoreResultPartial
	}
	failures := rep.Failures()
	switch rep.Result {
	case RestoreResultOK:
		rep.Summary = fmt.Sprintf("test restore passed: %d checks ok", len(rep.Checks))
	default:
		rep.Summary = fmt.Sprintf("test restore partial: %d of %d checks did not pass", len(failures), len(rep.Checks))
		if restore.Result == RestoreResultPartial && len(failures) == 0 {
			rep.Summary = fmt.Sprintf("test restore partial: every check passed, and the bundle itself records %d gap(s)", len(restore.Incomplete))
		}
	}
	rep.FinishedAt = time.Now().UTC()
	return rep, nil
}

// drillKeys picks the keys a drill unlocks with — the bundle's recovery kit,
// else (when allowed) this process's environment — and the ENCRYPTION_KEY
// string the journal chain key derives from.
func drillKeys(kit *RecoveryKit, useEnv bool) (keys map[string][]byte, source, chainSeed string) {
	if kit != nil {
		if k, err := kit.KeyMap(); err == nil && len(k) > 0 {
			return k, "recovery kit", kit.ChainSeed()
		}
	}
	if !useEnv {
		return nil, "none", ""
	}
	keys = map[string][]byte{}
	for _, v := range encryption.ConfiguredKeyVersions() {
		if rk, err := encryption.ResolveKey(v); err == nil {
			keys[v] = rk.Key
		}
	}
	if len(keys) == 0 {
		return nil, "none", ""
	}
	return keys, "environment", os.Getenv("ENCRYPTION_KEY")
}

func drillAttachments(ctx context.Context, db *sql.DB, outputDir string) DrillCheck {
	c := DrillCheck{Name: DrillCheckAttachments}
	rows, err := db.QueryContext(ctx, `SELECT DISTINCT workspace_id, sha256 FROM attachments`)
	if err != nil {
		if strings.Contains(err.Error(), "no such table") {
			c.Status, c.Detail = DrillStatusOK, "no attachments"
			return c
		}
		c.Status, c.Detail = DrillStatusFailed, err.Error()
		return c
	}
	defer func() { _ = rows.Close() }()
	var bad []string
	for rows.Next() {
		var ws, sha string
		if err := rows.Scan(&ws, &sha); err != nil {
			c.Status, c.Detail = DrillStatusFailed, err.Error()
			return c
		}
		c.Checked++
		if !validSha256Hex(sha) {
			c.Failed++
			continue
		}
		got, err := fileSHA(filepath.Join(outputDir, "attachments", ws, sha[:2], sha))
		if err != nil || got != sha {
			c.Failed++
			if len(bad) < 5 {
				bad = append(bad, sha[:12])
			}
		}
	}
	return finishCheck(c, "attachment file(s) open with the right content", "attachment file(s) missing or corrupt", bad)
}

func drillMemory(ctx context.Context, db *sql.DB) DrillCheck {
	c := DrillCheck{Name: DrillCheckMemory}
	rows, err := db.QueryContext(ctx, `SELECT DISTINCT sha256, payload_ref FROM memory_versions`)
	if err != nil {
		if strings.Contains(err.Error(), "no such table") {
			c.Status, c.Detail = DrillStatusOK, "no memory history"
			return c
		}
		c.Status, c.Detail = DrillStatusFailed, err.Error()
		return c
	}
	defer func() { _ = rows.Close() }()
	var bad []string
	for rows.Next() {
		var sha, ref string
		if err := rows.Scan(&sha, &ref); err != nil {
			c.Status, c.Detail = DrillStatusFailed, err.Error()
			return c
		}
		c.Checked++
		got, err := fileSHA(ref)
		if err != nil || got != sha {
			c.Failed++
			if len(bad) < 5 && len(sha) >= 12 {
				bad = append(bad, sha[:12])
			}
		}
	}
	return finishCheck(c, "memory version(s) load", "memory version(s) have no readable content", bad)
}

func drillCredentials(ctx context.Context, db *sql.DB, keys map[string][]byte, source string) DrillCheck {
	c := DrillCheck{Name: DrillCheckCredentials}
	var bad []string
	missingKey := map[string]bool{}
	_, err := EachSealedValue(ctx, db, func(v SealedValue) error {
		c.Checked++
		if keys == nil {
			c.Failed++
			return nil
		}
		if _, err := encryption.DecryptWithKeys(v.Envelope, keys); err != nil {
			c.Failed++
			if errors.Is(err, encryption.ErrNoKeyForVersion) {
				ver, _ := encryption.ParseEnvelopeVersion(v.Envelope)
				missingKey[ver] = true
			}
			if len(bad) < 5 {
				bad = append(bad, v.Table+"."+v.Column)
			}
		}
		return nil
	})
	if err != nil {
		c.Status, c.Detail = DrillStatusFailed, err.Error()
		return c
	}
	if c.Checked == 0 {
		c.Status, c.Detail = DrillStatusOK, "no sealed values"
		return c
	}
	if keys == nil {
		c.Status = DrillStatusSkipped
		c.Detail = fmt.Sprintf("%d sealed value(s) and no key to try: the bundle has no recovery kit (pass --env-keys to use this machine's ENCRYPTION_KEY)", c.Checked)
		return c
	}
	c = finishCheck(c, "sealed value(s) unlock with keys from the "+source, "sealed value(s) do not unlock", bad)
	if len(missingKey) > 0 {
		var vs []string
		for v := range missingKey {
			vs = append(vs, v)
		}
		encryption.SortVersions(vs)
		c.Detail += "; no key for " + strings.Join(vs, ", ")
	}
	return c
}

func drillJournal(ctx context.Context, db *sql.DB, chainSeed string) DrillCheck {
	c := DrillCheck{Name: DrillCheckJournal}
	if chainSeed == "" {
		c.Status = DrillStatusSkipped
		c.Detail = "the journal chain is keyed by ENCRYPTION_KEY, which the drill does not have"
		return c
	}
	chainKey := journal.DeriveChainKey(chainSeed)
	rows, err := db.QueryContext(ctx, `SELECT id FROM workspaces WHERE deleted_at IS NULL ORDER BY id`)
	if err != nil {
		c.Status, c.Detail = DrillStatusFailed, err.Error()
		return c
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err == nil {
			ids = append(ids, id)
		}
	}
	_ = rows.Close()
	var bad []string
	entries := 0
	for _, id := range ids {
		res, err := journal.VerifyChainWithKey(ctx, db, id, chainKey)
		c.Checked++
		if err != nil {
			c.Failed++
			bad = append(bad, id+": "+err.Error())
			continue
		}
		entries += res.Count
		if !res.OK {
			c.Failed++
			if len(bad) < 5 {
				bad = append(bad, fmt.Sprintf("%s: %s", id, res.Reason))
			}
		}
	}
	c = finishCheck(c, "workspace journal(s) verify", "workspace journal(s) do not verify", bad)
	if c.Status == DrillStatusOK {
		c.Detail = fmt.Sprintf("%d workspace journal(s) verify (%d entries)", c.Checked, entries)
	}
	return c
}

func drillHeld(ctx context.Context, db *sql.DB) DrillCheck {
	c := DrillCheck{Name: DrillCheckHeld, Checked: 1}
	holds, err := quiesce.ReadHolds(ctx, db)
	if err != nil {
		c.Status, c.Detail, c.Failed = DrillStatusFailed, err.Error(), 1
		return c
	}
	held := map[string]bool{}
	for _, h := range holds {
		held[h.Key] = true
	}
	for _, k := range []string{quiesce.HoldRoutines, quiesce.HoldWebhooks, quiesce.HoldQueue} {
		if !held[k] && !held[quiesce.HoldAll] {
			c.Status, c.Failed = DrillStatusFailed, 1
			c.Detail = k + " is not held after the restore"
			return c
		}
	}
	c.Status, c.Detail = DrillStatusOK, "routines, webhooks and queued work stay held"
	return c
}

func finishCheck(c DrillCheck, okText, badText string, examples []string) DrillCheck {
	if c.Failed == 0 {
		c.Status = DrillStatusOK
		if c.Checked == 0 {
			c.Detail = "nothing to check"
		} else {
			c.Detail = fmt.Sprintf("%d %s", c.Checked, okText)
		}
		return c
	}
	c.Status = DrillStatusFailed
	c.Detail = fmt.Sprintf("%d of %d %s", c.Failed, c.Checked, badText)
	if len(examples) > 0 {
		c.Detail += " (" + strings.Join(examples, ", ") + ")"
	}
	return c
}

func fileSHA(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
