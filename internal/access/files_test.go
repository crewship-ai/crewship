package access

import (
	"bytes"
	"errors"
	"testing"
)

func TestFileVersionsIsolateHumansAndRevokeImmediately(t *testing.T) {
	s := fixture(t)
	r := Right{"agent", "a", "run"}
	policy(t, s, "h1", r)
	policy(t, s, "h2", r)
	h, a, err := s.Admit(t.Context(), "h1", "w", "a", "c1", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	v, err := s.SaveFile(t.Context(), h, "reports/result.txt", []byte("H1_FILE_CANARY"))
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.SaveFile(t.Context(), h, v.Name, []byte("H1_FILE_CANARY"))
	if err != nil || again.ID != v.ID {
		t.Fatalf("idempotent save: %+v %v", again, err)
	}
	if _, err = s.SaveFile(t.Context(), h, v.Name, []byte("overwrite")); !errors.Is(err, ErrDenied) {
		t.Fatalf("overwrite: %v", err)
	}
	if _, err = s.SaveFile(t.Context(), a.ID, "forged.txt", nil); !errors.Is(err, ErrDenied) {
		t.Fatalf("public ID authority: %v", err)
	}
	if err = s.CompleteAttempt(t.Context(), h); err != nil {
		t.Fatal(err)
	}
	_, data, err := (Store{s.DB}).ReadFileForChat(t.Context(), "h1", "w", "a", "c1", v.ID)
	if err != nil || string(data) != "H1_FILE_CANARY" {
		t.Fatalf("completed output: %q %v", data, err)
	}
	for _, chat := range []string{"c1", "c2"} {
		if _, _, err = s.ReadFileForChat(t.Context(), "h2", "w", "a", chat, v.ID); !errors.Is(err, ErrDenied) {
			t.Fatalf("foreign download %s: %v", chat, err)
		}
	}
	list, err := s.FilesForChat(t.Context(), "h2", "w", "a", "c2")
	if err != nil || len(list) != 0 {
		t.Fatalf("foreign filename leak: %+v %v", list, err)
	}
	policy(t, s, "h1")
	if err = s.CheckFileForChat(t.Context(), "h1", "w", "a", "c1", v.ID); !errors.Is(err, ErrDenied) {
		t.Fatalf("revoked download: %v", err)
	}
	policy(t, s, "h1", r)
	list, err = s.FilesForChat(t.Context(), "h1", "w", "a", "c1")
	if err != nil || len(list) != 0 {
		t.Fatalf("regrant resurrected output: %+v %v", list, err)
	}
}

func TestFileNamesBoundsAndImmutableVersionStorage(t *testing.T) {
	s := fixture(t)
	policy(t, s, "h1", Right{"agent", "a", "run"})
	h, _, err := s.Admit(t.Context(), "h1", "w", "a", "c1", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"", "/absolute", "../escape", "a/../b", ".auth.json", "a/.secret", "a\\b", "x\nheader", "a//b"} {
		if _, err = s.SaveFile(t.Context(), h, name, nil); !errors.Is(err, ErrDenied) {
			t.Fatalf("unsafe name %q: %v", name, err)
		}
	}
	if _, err = s.SaveFile(t.Context(), h, "large", make([]byte, (1<<20)+1)); !errors.Is(err, ErrDenied) {
		t.Fatalf("oversize: %v", err)
	}
	v, err := s.SaveFile(t.Context(), h, "zero", []byte{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.Exec(`UPDATE access_files SET content=X'78',size_bytes=1 WHERE id=?`, v.ID); err == nil {
		t.Fatal("immutable version changed")
	}
	if _, err = s.DB.Exec(`INSERT INTO access_files(id,attempt_id,workspace_id,principal_id,scope,agent_id,name,content,size_bytes,sha256,created_at) SELECT 'bad',attempt_id,workspace_id,principal_id,scope,agent_id,'bad',content,1,sha256,created_at FROM access_files WHERE id=?`, v.ID); err == nil {
		t.Fatal("inconsistent size accepted")
	}
	block := bytes.Repeat([]byte("x"), 1<<20)
	for i := range 32 {
		if _, err = s.SaveFile(t.Context(), h, string(rune('a'+i))+".txt", block); err != nil {
			t.Fatalf("bounded capacity %d: %v", i, err)
		}
	}
	if _, err = s.SaveFile(t.Context(), h, "overflow", []byte("x")); err == nil {
		t.Fatal("actor capacity exceeded")
	}
}

func TestGroupFilesShareOnlyExplicitCurrentAudience(t *testing.T) {
	s := groupFixture(t)
	h, _, err := s.AdmitChat(t.Context(), "h1", "w", "a", "group", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	v, err := s.SaveFile(t.Context(), h, "shared.txt", []byte("GROUP_FILE_CANARY"))
	if err != nil {
		t.Fatal(err)
	}
	if err = s.CompleteAttempt(t.Context(), h); err != nil {
		t.Fatal(err)
	}
	_, data, err := s.ReadFileForChat(t.Context(), "h2", "w", "a", "group", v.ID)
	if err != nil || string(data) != "GROUP_FILE_CANARY" {
		t.Fatalf("explicit shared output: %q %v", data, err)
	}
	if _, _, err = s.ReadFileForChat(t.Context(), "h2", "w", "a", "c2", v.ID); !errors.Is(err, ErrDenied) {
		t.Fatalf("group into private: %v", err)
	}
	if _, err = s.DB.Exec(`DELETE FROM chat_participants WHERE chat_id='group' AND user_id='h2'`); err != nil {
		t.Fatal(err)
	}
	if err = s.CheckFileForChat(t.Context(), "h2", "w", "a", "group", v.ID); !errors.Is(err, ErrDenied) {
		t.Fatalf("removed group member: %v", err)
	}
}
