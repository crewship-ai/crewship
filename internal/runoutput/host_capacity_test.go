//go:build unix

package runoutput

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestHostCapacityReservationsSerializeAndCollectionReleases(t *testing.T) {
	input := hostFixtureInput(t)
	root := filepath.Dir(input.Directory)
	input.CapacityBytes = DefaultLimit + hostReservationAllowance
	input.CapacityRuns = 1
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, id := range []string{"one", "two"} {
		next := input
		next.Directory = filepath.Join(root, id)
		wg.Add(1)
		go func() { defer wg.Done(); results <- reserveHostCapacity(t.Context(), next) }()
	}
	wg.Wait()
	close(results)
	successes, full := 0, 0
	for err := range results {
		if err == nil {
			successes++
		} else if errors.Is(err, ErrHostCapacity) {
			full++
		} else {
			t.Fatal(err)
		}
	}
	if successes != 1 || full != 1 {
		t.Fatalf("success=%d full=%d", successes, full)
	}
	var claimed string
	for _, id := range []string{"one", "two"} {
		dir := filepath.Join(root, id)
		if _, err := os.Stat(dir); err == nil {
			claimed = dir
		}
	}
	store, err := Create(filepath.Join(claimed, "output"), DefaultLimit)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err = store.Finish(0, "exited"); err != nil {
		t.Fatal(err)
	}
	if _, err = ReplayHost(t.Context(), claimed, 1024, func(Line) error { return nil }, func(Snapshot) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if yes, err := CollectHost(t.Context(), claimed, time.Now().Add(time.Hour)); err != nil || !yes {
		t.Fatalf("collection=%v %v", yes, err)
	}
	next := input
	next.Directory = filepath.Join(root, "three")
	if err := reserveHostCapacity(t.Context(), next); err != nil {
		t.Fatalf("collected capacity not released: %v", err)
	}
	if err := reserveHostCapacity(t.Context(), HostInput{Directory: claimed, MaxBytes: DefaultLimit}); !errors.Is(err, ErrClaimed) {
		t.Fatalf("collected run relaunched: %v", err)
	}
}

func TestHostCapacityDoesNotDiscountUnresolvedClaim(t *testing.T) {
	input := hostFixtureInput(t)
	if err := os.Mkdir(filepath.Join(filepath.Dir(input.Directory), "unresolved"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := reserveHostCapacity(t.Context(), input); err == nil {
		t.Fatal("unresolved claim ignored")
	}
}

func TestHostRetentionResumesAfterLastSourceUnlinkBeforeReservationRelease(t *testing.T) {
	dir, _ := replayFixture(t, true)
	if _, err := ReplayHost(t.Context(), dir, 1024, func(Line) error { return nil }, func(Snapshot) error { return nil }); err != nil {
		t.Fatal(err)
	}
	ack, err := readReplayAcknowledgement(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err = writeCollectionReceipt(dir, ack); err != nil {
		t.Fatal(err)
	}
	if err = os.RemoveAll(filepath.Join(dir, "output")); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "reservation.json"), []byte(`{"version":1,"bytes":67108864}`), 0600); err != nil {
		t.Fatal(err)
	}
	if yes, err := CollectHost(t.Context(), dir, time.Now().Add(time.Hour)); err != nil || !yes {
		t.Fatalf("resume=%v %v", yes, err)
	}
	if yes, err := HostCollectionComplete(t.Context(), dir); err != nil || !yes {
		t.Fatalf("receipt=%v %v", yes, err)
	}
}

func TestHostCapacitySettlesOnlyValidatedTerminalOutput(t *testing.T) {
	input := hostFixtureInput(t)
	if err := reserveHostCapacity(t.Context(), input); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(input.Directory, "reservation.json")
	initial, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	store, err := Create(filepath.Join(input.Directory, "output"), DefaultLimit)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := shrinkHostReservation(t.Context(), input.Directory); err == nil {
		t.Fatal("incomplete reservation shrank")
	}
	unchanged, _ := os.ReadFile(path)
	if string(initial) != string(unchanged) {
		t.Fatal("incomplete reservation changed")
	}
	if err := store.Finish(0, "exited"); err != nil {
		t.Fatal(err)
	}
	if err := shrinkHostReservation(t.Context(), input.Directory); err != nil {
		t.Fatal(err)
	}
	settled, _ := os.ReadFile(path)
	var got struct {
		Bytes int64 `json:"bytes"`
	}
	if err := json.Unmarshal(settled, &got); err != nil {
		t.Fatal(err)
	}
	if got.Bytes < hostReservationAllowance || got.Bytes >= DefaultLimit {
		t.Fatalf("settled bytes=%d", got.Bytes)
	}
	next := input
	next.CapacityBytes = DefaultLimit + hostReservationAllowance + got.Bytes
	next.Directory = filepath.Join(filepath.Dir(input.Directory), "next")
	if err := reserveHostCapacity(t.Context(), next); err != nil {
		t.Fatalf("settled unused capacity not available: %v", err)
	}
}
