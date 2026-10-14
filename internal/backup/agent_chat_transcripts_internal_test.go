package backup

import (
	"archive/tar"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func transcriptPayload(t *testing.T, entries map[string]string) *ExtractedPayload {
	t.Helper()
	file, err := os.Create(filepath.Join(t.TempDir(), "transcripts.tar"))
	if err != nil {
		t.Fatal(err)
	}
	tw := tar.NewWriter(file)
	dump := &DBDump{AgentChatTranscripts: map[string]string{}, Tables: map[string][]map[string]any{}}
	for id, body := range entries {
		if err := tw.WriteHeader(&tar.Header{Name: id + ".jsonl", Typeflag: tar.TypeReg, Mode: 0o600, Size: int64(len(body))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
		dump.AgentChatTranscripts[id] = id
		dump.Tables["chats"] = append(dump.Tables["chats"], map[string]any{"id": id, "message_count": 1})
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	return &ExtractedPayload{DBDump: dump, agentChatsPath: file.Name()}
}

func TestTranscriptPublicationRollbackAndCommit(t *testing.T) {
	for _, committed := range []bool{false, true} {
		t.Run(map[bool]string{false: "rollback", true: "commit"}[committed], func(t *testing.T) {
			storage := t.TempDir()
			dir := filepath.Join(storage, "conversations")
			if err := os.Mkdir(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			old := "{\"session_id\":\"existing\",\"content\":\"live original\"}\n"
			if err := os.WriteFile(filepath.Join(dir, "existing.jsonl"), []byte(old), 0o600); err != nil {
				t.Fatal(err)
			}
			entries := map[string]string{"existing": "{\"session_id\":\"existing\",\"content\":\"backup\"}\n", "new": "{\"session_id\":\"new\",\"content\":\"new history\"}\n"}
			state, err := prepareAgentChatTranscripts(t.Context(), storage, transcriptPayload(t, entries), false, true)
			if err != nil {
				t.Fatal(err)
			}
			if err := state.publish(); err != nil {
				t.Fatal(err)
			}
			if err := state.finish(committed); err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(filepath.Join(dir, "existing.jsonl"))
			want := old
			if committed {
				want = entries["existing"]
			}
			if err != nil || string(got) != want {
				t.Fatalf("existing transcript = %q, want %q: %v", got, want, err)
			}
			_, err = os.Stat(filepath.Join(dir, "new.jsonl"))
			if (committed && err != nil) || (!committed && !os.IsNotExist(err)) {
				t.Fatalf("new transcript after committed=%v: %v", committed, err)
			}
			files, err := os.ReadDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			for _, file := range files {
				if strings.HasPrefix(file.Name(), ".restore-") {
					t.Fatalf("staging file leaked: %s", file.Name())
				}
			}
		})
	}
}

func TestTranscriptPreparationRejectsBadIdentityWithoutPublishing(t *testing.T) {
	for _, body := range []string{"invalid JSON\n", "{\"session_id\":\"foreign\"}\n"} {
		storage := t.TempDir()
		if _, err := prepareAgentChatTranscripts(t.Context(), storage, transcriptPayload(t, map[string]string{"allowed": body}), false, false); err == nil {
			t.Fatal("invalid transcript accepted")
		}
		files, err := os.ReadDir(filepath.Join(storage, "conversations"))
		if err != nil || len(files) != 0 {
			t.Fatalf("failed preparation leaked files: %v %v", files, err)
		}
	}
}

func TestTranscriptCollectionRejectsSymlinksAndReportsMissing(t *testing.T) {
	storage := t.TempDir()
	dir := filepath.Join(storage, "conversations")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	foreign := filepath.Join(t.TempDir(), "other.jsonl")
	if err := os.WriteFile(foreign, []byte("other tenant"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(foreign, filepath.Join(dir, "allowed.jsonl")); err != nil {
		t.Fatal(err)
	}
	dump := &DBDump{Tables: map[string][]map[string]any{"chats": {{"id": "allowed", "message_count": 1}}}}
	if _, err := writeAgentChatTranscripts(context.Background(), nil, storage, dump, nonZeroTime()); err == nil {
		t.Fatal("symlink transcript accepted")
	}
	missing, err := writeAgentChatTranscripts(context.Background(), nil, "", dump, nonZeroTime())
	if err != nil || missing != 1 || missingAgentTranscripts(dump) != 1 {
		t.Fatalf("missing transcript not reported: missing=%d err=%v", missing, err)
	}
}
