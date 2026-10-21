package api

import (
	"context"
	"testing"
)

func TestBackupTranscriptSnapshotLockPreservesLegacyRestoreFence(t *testing.T) {
	legacyCalls, snapshotCalls := 0, 0
	legacy := func(context.Context) (func(), error) { legacyCalls++; return func() {}, nil }
	snapshot := func(context.Context) (func(), error) { snapshotCalls++; return func() {}, nil }
	router := &Router{}
	WithTranscriptLock(legacy)(router)
	h := &BackupHandler{}
	h.SetTranscriptLock(router.transcriptLock)
	release, err := h.createTranscriptLock()(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	release()
	if legacyCalls != 1 {
		t.Fatal("legacy create callback compatibility lost")
	}
	WithTranscriptSnapshotLock(snapshot)(router)
	h.SetTranscriptSnapshotLock(router.transcriptSnapshotLock)
	release, err = h.createTranscriptLock()(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	release()
	release, err = h.lockTranscripts(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	release()
	if legacyCalls != 2 || snapshotCalls != 1 {
		t.Fatalf("create/restore fences confused: legacy%d snapshot%d", legacyCalls, snapshotCalls)
	}
}
