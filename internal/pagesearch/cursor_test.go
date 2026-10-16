package pagesearch

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

func TestCursorOpaqueBoundedAndViewerCredentialQueryScoped(t *testing.T) {
	store := NewCursorStore()
	now := time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	query, _ := ParseQuery("Needle SECOND")
	binding := CursorBinding{"viewer", strings.Repeat("a", 64), "workspace", query.Fingerprint()}
	token, err := store.Issue(binding, "visible-page")
	if err != nil {
		t.Fatal(err)
	}
	nonce, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(nonce) != 32 || strings.Contains(string(nonce), "visible-page") {
		t.Fatal("cursor carries structured identity")
	}
	if after, err := store.Resolve(token, binding); err != nil || after != "visible-page" {
		t.Fatalf("own cursor unavailable %s %v", after, err)
	}
	for name, mutate := range map[string]func(*CursorBinding){"viewer": func(b *CursorBinding) { b.ViewerID = "other" }, "credential": func(b *CursorBinding) { b.CredentialHash = strings.Repeat("b", 64) }, "workspace": func(b *CursorBinding) { b.WorkspaceID = "other" }, "query": func(b *CursorBinding) { b.QueryHash = strings.Repeat("b", 64) }} {
		t.Run(name, func(t *testing.T) {
			copy := binding
			mutate(&copy)
			if _, err := store.Resolve(token, copy); err == nil {
				t.Fatal("cross-scope cursor admitted")
			}
		})
	}
	equivalent, _ := ParseQuery(" second needle needle ")
	if equivalent.Fingerprint() != query.Fingerprint() {
		t.Fatal("semantic query normalization differs")
	}
	now = now.Add(CursorTTL)
	if _, err := store.Resolve(token, binding); err == nil {
		t.Fatal("expired cursor admitted")
	}
	for i := 0; i < MaxCursors+10; i++ {
		now = now.Add(time.Millisecond)
		if _, err := store.Issue(binding, "visible-page"); err != nil {
			t.Fatal(err)
		}
	}
	if len(store.rows) != MaxCursors {
		t.Fatalf("unbounded cursor storage %d", len(store.rows))
	}
}
