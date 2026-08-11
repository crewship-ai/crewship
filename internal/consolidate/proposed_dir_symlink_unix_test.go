//go:build unix

package consolidate

import (
	"bytes"
	"context"
	cryptorand "crypto/rand"
	"database/sql"
	"database/sql/driver"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

var cleanupSwapDriverSeq atomic.Uint64

type cleanupSwapDriver struct {
	proposalPath string
	victimPath   string
}

func (d cleanupSwapDriver) Open(string) (driver.Conn, error) {
	return &cleanupSwapConn{proposalPath: d.proposalPath, victimPath: d.victimPath}, nil
}

type cleanupSwapConn struct {
	proposalPath string
	victimPath   string
}

func (*cleanupSwapConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("unexpected Prepare")
}
func (*cleanupSwapConn) Close() error              { return nil }
func (*cleanupSwapConn) Begin() (driver.Tx, error) { return nil, errors.New("unexpected Begin") }
func (c *cleanupSwapConn) ExecContext(context.Context, string, []driver.NamedValue) (driver.Result, error) {
	if err := os.Remove(c.proposalPath); err != nil {
		return nil, err
	}
	if err := os.Symlink(c.victimPath, c.proposalPath); err != nil {
		return nil, err
	}
	return nil, errors.New("injected insert failure")
}
func symlinkedProposedDir(t *testing.T) (outputDir, outsideDir string) {
	t.Helper()
	outputDir = t.TempDir()
	outsideDir = t.TempDir()
	if err := os.Symlink(outsideDir, filepath.Join(outputDir, ".proposed")); err != nil {
		t.Fatalf("plant .proposed symlink: %v", err)
	}
	return outputDir, outsideDir
}

func assertDirEmpty(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("outside directory received %d entrie(s), want none", len(entries))
	}
}

func TestWriteProposalRefusesSymlinkedProposedDir(t *testing.T) {
	outputDir, outsideDir := symlinkedProposedDir(t)
	c := &Consolidator{Journal: &noopEmitter{}, Logger: quietLogger()}
	_, err := c.writeProposal(context.Background(), Config{
		WorkspaceID: "ws_test", CrewID: "crew_test", OutputDir: outputDir,
	}, time.Date(2026, 8, 10, 16, 0, 0, 0, time.UTC), []LearnedRule{{
		Pattern: "pattern", Action: "action",
	}}, 1)
	if err == nil {
		t.Fatal("writeProposal accepted a symlinked .proposed directory")
	}
	assertDirEmpty(t, outsideDir)
}

func TestWriteProposalRefusesMissingOutputDir(t *testing.T) {
	outputDir := filepath.Join(t.TempDir(), "topics")
	c := &Consolidator{Journal: &noopEmitter{}, Logger: quietLogger()}
	_, err := c.writeProposal(context.Background(), Config{
		WorkspaceID: "ws_test", CrewID: "crew_test", OutputDir: outputDir,
	}, time.Date(2026, 8, 10, 16, 0, 0, 0, time.UTC), []LearnedRule{{
		Pattern: "pattern", Action: "action",
	}}, 1)
	if err == nil {
		t.Fatal("writeProposal created an unanchored output directory")
	}
	if _, statErr := os.Lstat(outputDir); !os.IsNotExist(statErr) {
		t.Fatalf("output directory was created or stat failed unexpectedly: %v", statErr)
	}
}

func TestPromoteRuleToSkillRefusesSymlinkedProposedDir(t *testing.T) {
	outputDir, outsideDir := symlinkedProposedDir(t)
	_, err := PromoteRuleToSkill(LearnedRule{
		Pattern: "safe pattern", Action: "safe action",
	}, ScoreResult{}, SkillPromoteOptions{OutputDir: outputDir})
	if err == nil {
		t.Fatal("PromoteRuleToSkill accepted a symlinked .proposed directory")
	}
	assertDirEmpty(t, outsideDir)
}

func TestWriteProposalMarshalCleanupDoesNotFollowLeafSymlink(t *testing.T) {
	outputDir := t.TempDir()
	proposedDir := filepath.Join(outputDir, ".proposed")
	if err := os.Mkdir(proposedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(t.TempDir(), "victim.md")
	const original = "outside victim\n"
	if err := os.WriteFile(victim, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}

	// newProposalID consumes the first eight bytes; WriteFileDurable uses
	// the next eight for its temporary sibling name.
	oldReader := cryptorand.Reader
	cryptorand.Reader = bytes.NewReader(make([]byte, 16))
	t.Cleanup(func() { cryptorand.Reader = oldReader })
	now := time.Date(2026, 8, 11, 8, 0, 0, 0, time.UTC)
	proposalPath := filepath.Join(proposedDir, "proposal-20260811080000-0000000000000000.md")
	if err := os.Symlink(victim, proposalPath); err != nil {
		t.Fatal(err)
	}

	c := &Consolidator{Journal: &noopEmitter{}, Logger: quietLogger()}
	_, err := c.writeProposal(context.Background(), Config{
		WorkspaceID: "ws_test", CrewID: "crew_test", OutputDir: outputDir,
	}, now, []LearnedRule{{
		Pattern: "pattern", Action: "action", Confidence: math.NaN(),
	}}, 1)
	if err == nil || !strings.Contains(err.Error(), "marshal proposal evidence") {
		t.Fatalf("writeProposal error = %v, want marshal failure", err)
	}
	got, err := os.ReadFile(victim)
	if err != nil {
		t.Fatalf("read victim: %v", err)
	}
	if string(got) != original {
		t.Fatalf("victim changed through proposal symlink: got %q", got)
	}
	if _, err := os.Lstat(proposalPath); !os.IsNotExist(err) {
		t.Fatalf("proposal cleanup left path behind: %v", err)
	}
}

func TestWriteProposalInsertCleanupUnlinksLeafSymlinkNotVictim(t *testing.T) {
	outputDir := t.TempDir()
	proposedDir := filepath.Join(outputDir, ".proposed")
	if err := os.Mkdir(proposedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(t.TempDir(), "victim.md")
	const original = "outside victim\n"
	if err := os.WriteFile(victim, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}

	oldReader := cryptorand.Reader
	cryptorand.Reader = bytes.NewReader(make([]byte, 16))
	t.Cleanup(func() { cryptorand.Reader = oldReader })
	now := time.Date(2026, 8, 11, 8, 1, 0, 0, time.UTC)
	proposalPath := filepath.Join(proposedDir, "proposal-20260811080100-0000000000000000.md")
	driverName := "cleanup-swap-" + strconv.FormatUint(cleanupSwapDriverSeq.Add(1), 10)
	sql.Register(driverName, cleanupSwapDriver{proposalPath: proposalPath, victimPath: victim})
	db, err := sql.Open(driverName, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	c := &Consolidator{DB: db, Journal: &noopEmitter{}, Logger: quietLogger()}
	_, err = c.writeProposal(context.Background(), Config{
		WorkspaceID: "ws_test", CrewID: "crew_test", OutputDir: outputDir,
	}, now, []LearnedRule{{Pattern: "pattern", Action: "action"}}, 1)
	if err == nil || !strings.Contains(err.Error(), "insert memory_proposal") {
		t.Fatalf("writeProposal error = %v, want insert failure", err)
	}
	got, err := os.ReadFile(victim)
	if err != nil {
		t.Fatalf("read victim: %v", err)
	}
	if string(got) != original {
		t.Fatalf("cleanup followed proposal symlink: got %q", got)
	}
	if _, err := os.Lstat(proposalPath); !os.IsNotExist(err) {
		t.Fatalf("proposal cleanup left symlink behind: %v", err)
	}
}
