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

// Only chats selected by the completed database snapshot may contribute files.
// Pin descriptors and byte lengths while append/replace is fenced; no DB reads,
// content validation or compression takes place while holding that fence.
type pinnedAgentTranscript struct {
	id, workspace string
	file          *os.File
	size          int64
}
type pinnedAgentTranscripts struct {
	files   []pinnedAgentTranscript
	missing int
}

func (p *pinnedAgentTranscripts) Close() {
	if p == nil {
		return
	}
	for _, f := range p.files {
		_ = f.file.Close()
	}
	p.files = nil
}
func pinAgentChatTranscripts(ctx context.Context, storage string, dump *DBDump, lock func(context.Context) (func(), error)) (_ *pinnedAgentTranscripts, retErr error) {
	pinned := &pinnedAgentTranscripts{}
	defer func() {
		if retErr != nil {
			pinned.Close()
		}
	}()
	if dump == nil || len(dump.Tables["chats"]) == 0 {
		return pinned, nil
	}
	if lock != nil {
		release, err := lock(ctx)
		if err != nil {
			return nil, err
		}
		defer release()
	}
	var root *os.Root
	if storage != "" {
		base, err := os.OpenRoot(storage)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		if base != nil {
			defer base.Close()
			root, err = base.OpenRoot("conversations")
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				return nil, err
			}
		}
	}
	if root != nil {
		defer root.Close()
	}
	for _, row := range dump.Tables["chats"] {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		id, _ := row["id"].(string)
		workspace, _ := row["workspace_id"].(string)
		if _, err := safepath.ValidateComponent(id); err != nil || id == "" {
			return nil, fmt.Errorf("backup: invalid chat identity")
		}
		var f *os.File
		var err error
		if root != nil {
			f, err = openTranscript(root, id+".jsonl")
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		if f == nil {
			if transcriptMessageCount(row) > 0 {
				pinned.missing++
			}
			continue
		}
		info, err := f.Stat()
		if err != nil {
			_ = f.Close()
			return nil, err
		}
		pinned.files = append(pinned.files, pinnedAgentTranscript{id: id, workspace: workspace, file: f, size: info.Size()})
	}
	return pinned, nil
}

func (p *pinnedAgentTranscripts) write(ctx context.Context, dst *TarZstWriter, dump *DBDump, now time.Time) ([]IncompleteItem, error) {
	incomplete := []IncompleteItem{}
	if dump == nil {
		return incomplete, nil
	}
	dump.AgentChatTranscripts = map[string]string{}
	for _, f := range p.files {
		if err := ctx.Err(); err != nil {
			return incomplete, err
		}
		validSize, skipped, err := measurePinnedTranscript(ctx, io.NewSectionReader(f.file, 0, f.size), f.id)
		if err != nil {
			return incomplete, fmt.Errorf("backup: read chat %s transcript: %w", f.id, err)
		}
		var src io.Reader = io.NewSectionReader(f.file, 0, f.size)
		if skipped > 0 {
			incomplete = append(incomplete, IncompleteItem{Kind: "agent_chat_transcript_invalid_lines", Count: skipped, Workspace: f.workspace, Detail: fmt.Sprintf("Chat %s: skipped %d malformed or foreign-session transcript lines", f.id, skipped)})
			src = &validTranscriptReader{ctx: ctx, reader: bufio.NewReader(src), chatID: f.id}
		}
		if err = dst.WriteStream(agentChatsPrefix+f.id+".jsonl", 0o600, now, validSize, src); err != nil {
			return incomplete, err
		}
		dump.AgentChatTranscripts[f.id] = f.id
	}
	return incomplete, nil
}

// Low-level compatibility helper. Production uses the explicit pin/write split
// so the transcript fence ends before validation and streaming begin.
func writeAgentChatTranscripts(ctx context.Context, dst *TarZstWriter, storage string, dump *DBDump, now time.Time) (int, error) {
	pinned, err := pinAgentChatTranscripts(ctx, storage, dump, nil)
	if err != nil {
		return 0, err
	}
	defer pinned.Close()
	_, err = pinned.write(ctx, dst, dump, now)
	return pinned.missing, err
}

const maxBackupTranscriptLine = 64 << 20

// ReadSlice drains oversized/corrupt lines with bounded memory, so one damaged
// tail cannot either abort every future backup or allocate the entire file.
func readPinnedTranscriptLine(ctx context.Context, r *bufio.Reader) ([]byte, bool, error) {
	var line []byte
	oversized := false
	for {
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		fragment, err := r.ReadSlice('\n')
		if !oversized {
			if len(line)+len(fragment) > maxBackupTranscriptLine {
				line = nil
				oversized = true
			} else {
				line = append(line, fragment...)
			}
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if errors.Is(err, io.EOF) {
			if len(line) > 0 || oversized {
				return line, oversized, nil
			}
			return nil, false, io.EOF
		}
		return line, oversized, err
	}
}
func validPinnedTranscriptLine(line []byte, id string) bool {
	if len(bytes.TrimSpace(line)) == 0 {
		return true
	}
	var message struct {
		SessionID string `json:"session_id"`
	}
	return json.Unmarshal(line, &message) == nil && message.SessionID == id
}
func measurePinnedTranscript(ctx context.Context, src io.Reader, id string) (size int64, skipped int, err error) {
	reader := bufio.NewReader(src)
	for {
		line, oversized, readErr := readPinnedTranscriptLine(ctx, reader)
		if errors.Is(readErr, io.EOF) {
			return size, skipped, nil
		}
		if readErr != nil {
			return size, skipped, readErr
		}
		if oversized || !validPinnedTranscriptLine(line, id) {
			skipped++
			continue
		}
		size += int64(len(line))
	}
}

type validTranscriptReader struct {
	ctx     context.Context
	reader  *bufio.Reader
	chatID  string
	pending []byte
}

func (r *validTranscriptReader) Read(dst []byte) (int, error) {
	if len(dst) == 0 {
		return 0, nil
	}
	for len(r.pending) == 0 {
		line, oversized, err := readPinnedTranscriptLine(r.ctx, r.reader)
		if err != nil {
			return 0, err
		}
		if oversized || !validPinnedTranscriptLine(line, r.chatID) {
			continue
		}
		r.pending = line
	}
	n := copy(dst, r.pending)
	r.pending = r.pending[n:]
	return n, nil
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
	digest                 []byte
	inode                  os.FileInfo
}

type transcriptRestore struct {
	root   *os.Root
	stages []*transcriptStage
}

// Preparation validates every scoped message before any file is published.
// Files are published only at PreCommit's end, with rollback until DB commit.
func prepareAgentChatTranscripts(ctx context.Context, storage string, payload *ExtractedPayload, dryRun, replace bool) (*transcriptRestore, error) {
	state, err := stageAgentChatTranscripts(ctx, storage, payload, dryRun, nil)
	if err != nil {
		return nil, err
	}
	if err := state.reconcile(ctx, dryRun, replace); err != nil {
		return nil, errors.Join(err, state.finish(false))
	}
	return state, nil
}

// Staging only touches private temporary files. Reconcile must run under the
// live writer fence immediately before publication, after slow restore work.
func stageAgentChatTranscripts(ctx context.Context, storage string, payload *ExtractedPayload, dryRun bool, skip map[string]bool) (_ *transcriptRestore, retErr error) {
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
		if err := state.prepareOne(ctx, target, source, tr, payload.DBDump.chatTranscriptRemap, dryRun, skip[target]); err != nil {
			return nil, err
		}
	}
	if len(seen) != len(payload.DBDump.AgentChatTranscripts) {
		return nil, fmt.Errorf("backup: agent chat transcript section incomplete")
	}
	return state, nil
}

func (state *transcriptRestore) prepareOne(ctx context.Context, target, source string, src io.Reader, remap map[string]map[string]string, dryRun, skip bool) error {
	stage := &transcriptStage{target: target + ".jsonl"}
	if !skip {
		state.stages = append(state.stages, stage)
	}
	var dst io.Writer = io.Discard
	var temp *os.File
	if !dryRun && !skip {
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
		changed := target != source
		if changed {
			message["session_id"], _ = json.Marshal(target)
		}
		for field, table := range map[string]string{"agent_id": "agents", "author_user_id": "users"} {
			var id string
			_ = json.Unmarshal(message[field], &id)
			if mapped := remap[table][id]; mapped != "" && mapped != id {
				message[field], _ = json.Marshal(mapped)
				changed = true
			}
		}
		beforeMetadata := message["metadata"]
		if err := remapTranscriptPageContext(message, remap); err != nil {
			return err
		}
		changed = changed || !bytes.Equal(beforeMetadata, message["metadata"])
		if changed {
			var err error
			line, err = json.Marshal(message)
			if err != nil {
				return err
			}
		}
		if err := hashTranscriptLine(hash, line); err != nil {
			return err
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
	stage.digest = hash.Sum(nil)
	return nil
}

func (state *transcriptRestore) reconcile(ctx context.Context, dryRun, replace bool) error {
	if state.root == nil {
		return nil
	}
	for _, stage := range state.stages {
		if err := ctx.Err(); err != nil {
			return err
		}
		existing, err := openTranscript(state.root, stage.target)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		other, digestErr := transcriptDigest(existing)
		_ = existing.Close()
		if digestErr == nil && bytes.Equal(stage.digest, other) {
			if stage.temp != "" {
				if err := state.root.Remove(stage.temp); err != nil {
					return err
				}
				stage.temp = ""
			}
			continue
		}
		if !replace {
			return fmt.Errorf("backup: existing agent chat transcript %s differs; left untouched", stage.target)
		}
		if !dryRun {
			stage.previous = ".restore-original-" + newRemapCUID()
			if err := state.root.Link(stage.target, stage.previous); err != nil {
				return err
			}
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
