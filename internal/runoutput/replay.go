package runoutput

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type replayAcknowledgement struct {
	Version  int    `json:"version"`
	Sequence uint64 `json:"sequence"`
	Index    uint32 `json:"index"`
	Hash     string `json:"hash"`
	Terminal bool   `json:"terminal"`
}

// ReplayHost rebuilds framing from the beginning of the bounded immutable log.
// project must durably and idempotently project each deterministic line key
// (run identity, Sequence, Index). A crash after project commits but before the
// acknowledgement is synced delivers that SAME key again. This function does
// not make external side effects transactional. complete has the same contract
// for the terminal sequence and is called only after full prefix validation.
func ReplayHost(ctx context.Context, dir string, maxLineBytes int, project func(Line) error, complete func(Snapshot) error) (Snapshot, error) {
	var snapshot Snapshot
	if project == nil || complete == nil {
		return snapshot, errors.New("replay requires line and terminal projections")
	}
	if err := validateHostDirectory(dir); err != nil {
		return snapshot, err
	}
	release, err := lockHostReplay(ctx, dir)
	if err != nil {
		return snapshot, err
	}
	defer release()
	return replayHostLocked(ctx, dir, maxLineBytes, project, complete)
}

func replayHostLocked(ctx context.Context, dir string, maxLineBytes int, project func(Line) error, complete func(Snapshot) error) (Snapshot, error) {
	if err := cleanupHostTemps(dir); err != nil {
		return Snapshot{}, err
	}
	ack, err := readReplayAcknowledgement(dir)
	if err != nil {
		return Snapshot{}, err
	}
	hash := sha256.New()
	var sequence uint64
	var prefixHash string
	verifiedAck := ack.Sequence == 0
	decoder, err := NewLineDecoder(maxLineBytes, func(line Line) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if (ack.Terminal && line.Sequence <= ack.Sequence) || ack.Sequence > line.Sequence || (ack.Sequence == line.Sequence && ack.Index >= line.Index) {
			return nil
		}
		if ack.Terminal {
			return errors.New("retained source extended after terminal acknowledgement")
		}
		if err := project(line); err != nil {
			return err
		}
		next := replayAcknowledgement{Version: 1, Sequence: line.Sequence, Index: line.Index, Hash: prefixHash}
		if err := writeReplayAcknowledgement(dir, next); err != nil {
			return err
		}
		ack = next
		return nil
	})
	if err != nil {
		return Snapshot{}, err
	}
	var terminal *Result
	last, done, err := ReadCommitted(filepath.Join(dir, "output"), 0, func(record Record) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		sequence = record.Sequence
		data, err := json.Marshal(record)
		if err != nil {
			return err
		}
		_, _ = hash.Write(data)
		_, _ = hash.Write([]byte{'\n'})
		prefixHash = hex.EncodeToString(hash.Sum(nil))
		if record.Sequence == ack.Sequence {
			if prefixHash != ack.Hash {
				return errors.New("acknowledged retained prefix changed")
			}
			verifiedAck = true
		}
		// Older acknowledged prefixes must have been verified before any new
		// downstream effect is attempted, even when there were no earlier lines.
		if record.Sequence > ack.Sequence && !verifiedAck {
			return errors.New("retained acknowledgement beyond available prefix")
		}
		if err = decoder.Push(record); err != nil {
			return err
		}
		if record.Kind == "exit" {
			terminal = &Result{ExitCode: record.ExitCode, Reason: record.Reason}
			snapshot := Snapshot{Version: 1, Sequence: record.Sequence, Complete: true, Result: terminal}
			if ack.Terminal {
				if ack.Sequence != record.Sequence {
					return errors.New("retained terminal acknowledgement changed")
				}
				return nil
			}
			if err = complete(snapshot); err != nil {
				return err
			}
			next := replayAcknowledgement{Version: 1, Sequence: record.Sequence, Hash: prefixHash, Terminal: true}
			if err = writeReplayAcknowledgement(dir, next); err != nil {
				return err
			}
			ack = next
		}
		return nil
	})
	if err != nil {
		return Snapshot{}, err
	}
	if !verifiedAck || ack.Sequence > sequence {
		return Snapshot{}, errors.New("retained acknowledgement exceeds source")
	}
	snapshot := Snapshot{Version: 1, Sequence: last, Complete: done, Result: terminal}
	if err = snapshot.Validate(); err != nil {
		return Snapshot{}, err
	}
	return snapshot, nil
}

func readReplayAcknowledgement(dir string) (replayAcknowledgement, error) {
	return readAcknowledgementFile(filepath.Join(dir, "replay-ack.json"))
}

func readAcknowledgementFile(path string) (replayAcknowledgement, error) {
	var ack replayAcknowledgement
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return ack, nil
	}
	if err != nil {
		return ack, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 4097))
	if err != nil {
		return ack, err
	}
	if len(data) > 4096 {
		return ack, errors.New("oversized retained acknowledgement")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&ack); err != nil {
		return ack, err
	}
	var extra any
	if err = decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return ack, errors.New("trailing retained acknowledgement")
	}
	digest, err := hex.DecodeString(ack.Hash)
	if err != nil || len(digest) != sha256.Size || ack.Version != 1 || ack.Sequence == 0 || (ack.Terminal && ack.Sequence < 2) {
		return ack, errors.New("invalid retained acknowledgement")
	}
	return ack, nil
}

func writeReplayAcknowledgement(dir string, ack replayAcknowledgement) error {
	data, err := json.Marshal(ack)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".replay-ack-*")
	if err != nil {
		return err
	}
	temp := f.Name()
	defer os.Remove(temp)
	_, writeErr := f.Write(data)
	syncErr := f.Sync()
	closeErr := f.Close()
	if err = errors.Join(writeErr, syncErr, closeErr); err != nil {
		return err
	}
	if err = os.Rename(temp, filepath.Join(dir, "replay-ack.json")); err != nil {
		return err
	}
	return syncDir(dir)
}

// CollectHost removes retained output only after terminal projection was
// acknowledged and that acknowledgement predates cutoff. The outer directory,
// lock and terminal receipt remain as a duplicate-launch tombstone. An absent
// source after successful collection is not permission to launch again.
func CollectHost(ctx context.Context, dir string, cutoff time.Time) (bool, error) {
	if cutoff.IsZero() {
		return false, errors.New("retention requires explicit cutoff")
	}
	if err := validateHostDirectory(dir); err != nil {
		return false, err
	}
	release, err := lockHostReplay(ctx, dir)
	if err != nil {
		return false, err
	}
	defer release()
	if err := cleanupHostTemps(dir); err != nil {
		return false, err
	}
	ack, err := readReplayAcknowledgement(dir)
	if err != nil {
		return false, err
	}
	if !ack.Terminal {
		return false, nil
	}
	info, err := os.Stat(filepath.Join(dir, "replay-ack.json"))
	if err != nil {
		return false, err
	}
	if !info.ModTime().Before(cutoff) {
		return false, nil
	}
	output := filepath.Join(dir, "output")
	collecting := filepath.Join(dir, ".collecting-output")
	staged, stageErr := os.Lstat(collecting)
	if stageErr != nil && !errors.Is(stageErr, os.ErrNotExist) {
		return false, stageErr
	}
	if stageErr == nil {
		if err = verifyCollectionReceipt(dir, ack); err != nil {
			return false, err
		}
		if !staged.IsDir() || staged.Mode()&os.ModeSymlink != 0 {
			return false, errors.New("invalid retained collection directory")
		}
	} else {
		if _, err = os.Lstat(output); errors.Is(err, os.ErrNotExist) {
			if err = verifyCollectionReceipt(dir, ack); err != nil {
				return false, err
			}
			changed, err := finishCollection(dir)
			return changed, err
		} else if err != nil {
			return false, err
		}
		// Verify the source before atomically moving it out of the replay path.
		// The rename makes collection resumable after a crash between unlinks.
		snapshot, err := replayHostLocked(ctx, dir, 16*1024*1024, func(Line) error { return errors.New("retention encountered unacknowledged output") }, func(Snapshot) error { return errors.New("retention encountered unacknowledged terminal") })
		if err != nil {
			return false, err
		}
		if !snapshot.Complete || snapshot.Result.Reason != "exited" {
			return false, nil
		}
		if err = ctx.Err(); err != nil {
			return false, err
		}
		// Persist permission to resume deletion before losing source validation.
		if err = writeCollectionReceipt(dir, ack); err != nil {
			return false, err
		}
		if err = os.Rename(output, collecting); err != nil {
			return false, err
		}
		if err = syncDir(dir); err != nil {
			return false, err
		}
	}
	output = collecting
	entries, err := os.ReadDir(output)
	if err != nil {
		return false, err
	}
	for _, entry := range entries {
		name := entry.Name()
		if name != "events.jsonl" && name != "checkpoint.json" && name != "checkpoint.tmp" {
			return false, errors.New("unexpected retained output file")
		}
		info, err := entry.Info()
		if err != nil {
			return false, err
		}
		if !info.Mode().IsRegular() {
			return false, errors.New("nonregular retained output file")
		}
	}
	for _, entry := range entries {
		if err = os.Remove(filepath.Join(output, entry.Name())); err != nil {
			return false, err
		}
	}
	if err = os.Remove(output); err != nil {
		return false, err
	}
	_, err = finishCollection(dir)
	return true, err
}

func writeCollectionReceipt(dir string, ack replayAcknowledgement) error {
	data, err := json.Marshal(ack)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".collection-*")
	if err != nil {
		return err
	}
	temporary := f.Name()
	defer os.Remove(temporary)
	_, writeErr := f.Write(data)
	syncErr := f.Sync()
	closeErr := f.Close()
	if err = errors.Join(writeErr, syncErr, closeErr); err != nil {
		return err
	}
	if err = os.Rename(temporary, filepath.Join(dir, "collection.json")); err != nil {
		return err
	}
	return syncDir(dir)
}

func verifyCollectionReceipt(dir string, ack replayAcknowledgement) error {
	receipt, err := readAcknowledgementFile(filepath.Join(dir, "collection.json"))
	if err != nil {
		return err
	}
	if !ack.Terminal || receipt != ack {
		return errors.New("missing or mismatched retained collection receipt")
	}
	return nil
}

func finishCollection(dir string) (bool, error) {
	changed := false
	for _, name := range []string{"identity.json", "reservation.json"} {
		err := os.Remove(filepath.Join(dir, name))
		if err == nil {
			changed = true
		} else if !errors.Is(err, os.ErrNotExist) {
			return false, err
		}
	}
	return changed, syncDir(dir)
}

// HostCollectionComplete validates the protected deletion receipt and absence
// of source and reservation. This permits controller state acknowledgement
// after a crash, without mistaking a missing source for successful collection.
func HostCollectionComplete(ctx context.Context, dir string) (bool, error) {
	if err := validateHostDirectory(dir); err != nil {
		return false, err
	}
	release, err := lockHostReplay(ctx, dir)
	if err != nil {
		return false, err
	}
	defer release()
	if err := cleanupHostTemps(dir); err != nil {
		return false, err
	}
	ack, err := readReplayAcknowledgement(dir)
	if err != nil {
		return false, err
	}
	if err = verifyCollectionReceipt(dir, ack); err != nil {
		return false, err
	}
	for _, name := range []string{"output", ".collecting-output", "identity.json", "reservation.json"} {
		_, err = os.Lstat(filepath.Join(dir, name))
		if err == nil {
			return false, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return false, err
		}
	}
	return true, nil
}

// cleanupHostTemps runs only with the per-run replay lock held. These exact
// private temporary prefixes belong to cursor/collection/reservation atomic
// writes; a crash can leave one behind. Unknown files are never swept.
func cleanupHostTemps(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	changed := false
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, ".replay-ack-") && !strings.HasPrefix(name, ".collection-") && !strings.HasPrefix(name, ".reservation-") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
			return errors.New("unexpected retained temporary file")
		}
		if err := os.Remove(filepath.Join(dir, name)); err != nil {
			return err
		}
		changed = true
	}
	if changed {
		return syncDir(dir)
	}
	return nil
}
