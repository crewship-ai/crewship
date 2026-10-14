package backup

import (
	"archive/tar"
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/safepath"
)

const agentChatsPrefix = "agent-chats/"

// Only chats selected by the database snapshot may contribute host files.
// The caller holds LockTranscripts across the DB snapshot and collection.
// Low-level callers must provide the same coordination or a stopped store.
func writeAgentChatTranscripts(ctx context.Context, dst *TarZstWriter, storage string, dump *DBDump, now time.Time) (int, error) {
	if dump == nil {
		return 0, nil
	}
	dump.AgentChatTranscripts = map[string]string{}
	var root *os.Root
	if storage != "" {
		base, err := os.OpenRoot(storage)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return 0, err
		}
		if base != nil {
			defer base.Close()
			root, err = base.OpenRoot("conversations")
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				return 0, err
			}
		}
	}
	if root != nil {
		defer root.Close()
	}
	missing := 0
	for _, row := range dump.Tables["chats"] {
		if err := ctx.Err(); err != nil {
			return missing, err
		}
		id, _ := row["id"].(string)
		if _, err := safepath.ValidateComponent(id); err != nil || id == "" {
			return missing, fmt.Errorf("backup: invalid chat identity")
		}
		var f *os.File
		var err error
		if root != nil {
			f, err = openTranscript(root, id+".jsonl")
		}
		if f == nil || errors.Is(err, os.ErrNotExist) {
			if transcriptMessageCount(row) > 0 {
				missing++
			}
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				return missing, err
			}
			continue
		}
		info, err := f.Stat()
		if err == nil {
			err = validateAgentTranscript(io.NewSectionReader(f, 0, info.Size()), id)
		}
		if err == nil {
			err = dst.WriteStream(agentChatsPrefix+id+".jsonl", 0o600, now, info.Size(), f)
		}
		_ = f.Close()
		if err != nil {
			return missing, err
		}
		dump.AgentChatTranscripts[id] = id
	}
	return missing, nil
}

// Root prevents escapes; comparing lstat with the opened inode also rejects
// symlinks and replacement races into another chat inside that root.
func openTranscript(root *os.Root, name string) (*os.File, error) {
	before, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() {
		return nil, fmt.Errorf("backup: chat transcript must be a regular file")
	}
	f, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	after, err := f.Stat()
	if err != nil || !os.SameFile(before, after) {
		_ = f.Close()
		return nil, fmt.Errorf("backup: chat transcript changed identity")
	}
	return f, nil
}

type transcriptStage struct {
	target, temp, previous string
	published              bool
	inode                  os.FileInfo
}

type transcriptRestore struct {
	root   *os.Root
	stages []*transcriptStage
}

// Preparation validates every scoped message before any file is published.
// Files are published only at PreCommit's end, with rollback until DB commit.
func prepareAgentChatTranscripts(ctx context.Context, storage string, payload *ExtractedPayload, dryRun, replace bool) (_ *transcriptRestore, retErr error) {
	state := &transcriptRestore{}
	defer func() {
		if retErr != nil {
			retErr = errors.Join(retErr, state.finish(false))
		}
	}()
	if payload.DBDump == nil || len(payload.DBDump.AgentChatTranscripts) == 0 {
		return state, nil
	}
	if storage == "" || payload.agentChatsPath == "" {
		return nil, fmt.Errorf("backup: agent chat transcript storage or section missing")
	}
	allowed := map[string]bool{}
	for _, row := range payload.DBDump.Tables["chats"] {
		id, _ := row["id"].(string)
		allowed[id] = true
	}
	f, err := payload.storageOrDefault().Open(ctx, payload.agentChatsPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if !dryRun {
		if err := os.MkdirAll(storage, 0o700); err != nil {
			return nil, err
		}
	}
	base, err := os.OpenRoot(storage)
	if err != nil && (!dryRun || !errors.Is(err, os.ErrNotExist)) {
		return nil, err
	}
	if base != nil {
		defer base.Close()
		if !dryRun {
			if err := base.Mkdir("conversations", 0o700); err != nil && !errors.Is(err, os.ErrExist) {
				return nil, err
			}
		}
		state.root, err = base.OpenRoot("conversations")
		if err != nil && (!dryRun || !errors.Is(err, os.ErrNotExist)) {
			return nil, err
		}
	}
	seen := map[string]bool{}
	tr := tar.NewReader(f)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		source := strings.TrimSuffix(hdr.Name, ".jsonl")
		target := payload.DBDump.AgentChatTranscripts[source]
		if _, err := safepath.ValidateComponent(source); err != nil || source == "" || hdr.Name != source+".jsonl" || hdr.Typeflag != tar.TypeReg || !allowed[target] || seen[source] {
			return nil, fmt.Errorf("backup: invalid or unscoped agent chat transcript")
		}
		if _, err := safepath.ValidateComponent(target); err != nil || target == "" {
			return nil, fmt.Errorf("backup: invalid restored chat identity")
		}
		seen[source] = true
		if err := state.prepareOne(ctx, target, source, tr, payload.DBDump.chatTranscriptRemap, dryRun, replace); err != nil {
			return nil, err
		}
	}
	if len(seen) != len(payload.DBDump.AgentChatTranscripts) {
		return nil, fmt.Errorf("backup: agent chat transcript section incomplete")
	}
	return state, nil
}

func (state *transcriptRestore) prepareOne(ctx context.Context, target, source string, src io.Reader, remap map[string]map[string]string, dryRun, replace bool) error {
	stage := &transcriptStage{target: target + ".jsonl"}
	state.stages = append(state.stages, stage)
	var dst io.Writer = io.Discard
	var temp *os.File
	if !dryRun {
		stage.temp = ".restore-" + newRemapCUID()
		var err error
		temp, err = state.root.OpenFile(stage.temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return err
		}
		defer temp.Close()
		dst = temp
	}
	hash := sha256.New()
	dst = io.MultiWriter(dst, hash)
	scanner := bufio.NewScanner(src)
	scanner.Buffer(make([]byte, 64<<10), 64<<20)
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return err
		}
		if len(bytes.TrimSpace(scanner.Bytes())) == 0 {
			if _, err := dst.Write(append(append([]byte(nil), scanner.Bytes()...), '\n')); err != nil {
				return err
			}
			continue
		}
		var message map[string]json.RawMessage
		if err := json.Unmarshal(scanner.Bytes(), &message); err != nil || message == nil {
			return fmt.Errorf("backup: invalid agent chat transcript message")
		}
		var chat string
		if err := json.Unmarshal(message["session_id"], &chat); err != nil || chat != source {
			return fmt.Errorf("backup: transcript message belongs to another chat")
		}
		line := scanner.Bytes()
		if target != source || len(remap["users"]) > 0 || len(remap["agents"]) > 0 {
			message["session_id"], _ = json.Marshal(target)
			for field, table := range map[string]string{"agent_id": "agents", "author_user_id": "users"} {
				var id string
				_ = json.Unmarshal(message[field], &id)
				if mapped := remap[table][id]; mapped != "" {
					message[field], _ = json.Marshal(mapped)
				}
			}
			var err error
			line, err = json.Marshal(message)
			if err != nil {
				return err
			}
		}
		if _, err := dst.Write(append(append([]byte(nil), line...), '\n')); err != nil {
			return err
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if temp != nil {
		if err := temp.Sync(); err != nil {
			return err
		}
		var err error
		stage.inode, err = temp.Stat()
		if err != nil {
			return err
		}
	}
	if state.root == nil {
		return nil
	}
	existing, err := openTranscript(state.root, stage.target)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer existing.Close()
	other := sha256.New()
	if _, err := io.Copy(other, existing); err != nil {
		return err
	}
	if bytes.Equal(hash.Sum(nil), other.Sum(nil)) {
		if stage.temp != "" {
			if err := state.root.Remove(stage.temp); err != nil {
				return err
			}
			stage.temp = ""
		}
		return nil
	}
	if !replace {
		return fmt.Errorf("backup: existing agent chat transcript differs; left untouched")
	}
	if !dryRun {
		stage.previous = ".restore-original-" + newRemapCUID()
		if err := state.root.Link(stage.target, stage.previous); err != nil {
			return err
		}
	}
	return nil
}

func (state *transcriptRestore) publish() error {
	for _, stage := range state.stages {
		if stage.temp == "" {
			continue
		}
		var err error
		if stage.previous != "" {
			err = state.root.Rename(stage.temp, stage.target)
		} else {
			err = state.root.Link(stage.temp, stage.target)
		}
		if err != nil {
			return err
		}
		stage.published = true
	}
	return state.syncDirectory()
}

func (state *transcriptRestore) finish(committed bool) error {
	if state == nil || state.root == nil {
		return nil
	}
	defer state.root.Close()
	var errs []error
	for i := len(state.stages) - 1; i >= 0; i-- {
		stage := state.stages[i]
		keepPrevious := false
		if stage.published && !committed {
			// Never remove an unrelated writer's replacement if coordination was
			// not provided by a low-level caller.
			current, err := state.root.Lstat(stage.target)
			if err != nil || !os.SameFile(stage.inode, current) {
				errs = append(errs, fmt.Errorf("backup: restored transcript changed before rollback"))
				keepPrevious = true
			} else if stage.previous != "" {
				if err := state.root.Rename(stage.previous, stage.target); err != nil {
					errs = append(errs, err)
					keepPrevious = true
				}
			} else if err := state.root.Remove(stage.target); err != nil {
				errs = append(errs, err)
			}
		}
		for _, name := range []string{stage.temp, stage.previous} {
			if name != "" && !(keepPrevious && name == stage.previous) {
				if err := state.root.Remove(name); err != nil && !errors.Is(err, os.ErrNotExist) {
					errs = append(errs, err)
				}
			}
		}
	}
	errs = append(errs, state.syncDirectory())
	return errors.Join(errs...)
}

func transcriptMessageCount(row map[string]any) float64 {
	n, _ := strconv.ParseFloat(fmt.Sprint(row["message_count"]), 64)
	return n
}

func missingAgentTranscripts(dump *DBDump) int {
	if dump == nil {
		return 0
	}
	included := map[string]bool{}
	for _, id := range dump.AgentChatTranscripts {
		included[id] = true
	}
	missing := 0
	for _, row := range dump.Tables["chats"] {
		id, _ := row["id"].(string)
		if transcriptMessageCount(row) > 0 && !included[id] {
			missing++
		}
	}
	return missing
}

func validateAgentTranscript(src io.Reader, id string) error {
	scanner := bufio.NewScanner(src)
	scanner.Buffer(make([]byte, 64<<10), 64<<20)
	for scanner.Scan() {
		if len(bytes.TrimSpace(scanner.Bytes())) == 0 {
			continue
		}
		var msg struct {
			SessionID string `json:"session_id"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil || msg.SessionID != id {
			return fmt.Errorf("backup: invalid or mismatched agent chat transcript")
		}
	}
	return scanner.Err()
}

func (state *transcriptRestore) syncDirectory() error {
	if state == nil || state.root == nil {
		return nil
	}
	dir, err := state.root.Open(".")
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
