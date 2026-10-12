package sidecar

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRunJournalCompactionCannotTruncatePreexistingSymlinkTarget(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "owned.runs")
	canary := filepath.Join(dir, "unrelated")
	if err := os.WriteFile(canary, []byte("unrelated bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(canary, path+".compact"); err != nil {
		t.Fatal(err)
	}
	r := newRunRegistry()
	now := time.Now().UTC()
	r.runs["finished"] = &runState{AgentID: "agent", StartedAt: now.Add(-time.Minute), EndedAt: now}
	r.journal = &runJournal{path: path, records: runJournalMaxRecords + 1}
	r.compact()
	got, err := os.ReadFile(canary)
	if err != nil || string(got) != "unrelated bytes" {
		t.Fatalf("compaction followed preexisting temporary symlink: %q %v", got, err)
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		t.Fatalf("published journal is not an owned regular file: %v %v", info, err)
	}
	records, err := r.journal.load()
	if err != nil || len(records) != 2 || records[0].Op != runRecordStart || records[1].Op != runRecordEnd {
		t.Fatalf("compaction lost terminal run history: %+v %v", records, err)
	}
	if r.journal.records != 2 {
		t.Fatalf("record accounting=%d", r.journal.records)
	}
}

func TestRunJournalCompactionPreservesStateOnStorageRefusal(t *testing.T) {
	for _, mode := range []string{"missing parent", "destination directory"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "journal")
			if mode == "missing parent" {
				path = filepath.Join(dir, "absent", "journal")
			} else if err := os.Mkdir(path, 0700); err != nil {
				t.Fatal(err)
			}
			r := newRunRegistry()
			r.runs["live"] = &runState{AgentID: "agent", StartedAt: time.Now()}
			r.journal = &runJournal{path: path, records: runJournalMaxRecords + 1}
			r.compact()
			if !r.journal.broken || r.journal.records != runJournalMaxRecords+1 || len(r.runs) != 1 {
				t.Fatal("failed compaction discarded state or reset record count")
			}
			entries, err := os.ReadDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			for _, e := range entries {
				if !e.IsDir() {
					t.Fatalf("failed compaction leaked temporary file: %s", e.Name())
				}
			}
		})
	}
}
