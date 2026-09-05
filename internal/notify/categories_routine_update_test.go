package notify

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A routine's progress notice and a chat reply are both kind=message, and only
// payload.subkind tells them apart. Mapping by kind alone filed every "step
// build finished" under chat.replies — the category a person tunes for "an
// agent answered me while I was away" — so muting one muted the other and the
// routines.* rows they subscribed to never arrived.
func TestCategoryForItem_SeparatesRoutineUpdatesFromChatReplies(t *testing.T) {
	update := map[string]interface{}{"subkind": SubkindRoutineUpdate, "pipeline_run_id": "r1"}
	if got := CategoryForItem("message", update); got != CategoryRoutinesCompleted {
		t.Errorf("routine update → %q, want %q", got, CategoryRoutinesCompleted)
	}

	reply := map[string]interface{}{"chat_url": "/chat/atlas"}
	if got := CategoryForItem("message", reply); got != CategoryChatReplies {
		t.Errorf("chat reply → %q, want %q", got, CategoryChatReplies)
	}

	// No payload at all must not panic, and must not become a routine update.
	if got := CategoryForItem("message", nil); got != CategoryChatReplies {
		t.Errorf("bare message → %q, want %q", got, CategoryChatReplies)
	}

	// Every other kind is unambiguous and keeps mapping by kind.
	if got := CategoryForItem("waitpoint", update); got != CategoryAgentsApproval {
		t.Errorf("waitpoint → %q, want %q", got, CategoryAgentsApproval)
	}
}

// The discriminator is a bare string in the producer (internal/pipeline) and a
// constant here, because inbox is a leaf package neither may import through.
// Two spellings of one fact drift silently, so this reads the producer.
func TestRoutineUpdateSubkindMatchesProducer(t *testing.T) {
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	root := dir
	for i := 0; i < 8; i++ {
		if _, err := os.Stat(filepath.Join(root, "go.mod")); err == nil {
			break
		}
		root = filepath.Dir(root)
	}
	src, err := os.ReadFile(filepath.Join(root, "internal/pipeline/runner_notify.go"))
	if err != nil {
		t.Fatalf("read producer: %v", err)
	}
	if !strings.Contains(string(src), `"subkind":         "`+SubkindRoutineUpdate+`"`) {
		t.Fatalf("runner_notify.go no longer writes subkind=%q — the category mapping keys off it",
			SubkindRoutineUpdate)
	}
}

// TestDigestSubkindMatchesProducer is SubkindDigest's half of the same drift
// guard: internal/inbox/digest.go's DigestSubkind constant is the producer,
// duplicated here as a literal for the leaf-package reason both doc comments
// give.
func TestDigestSubkindMatchesProducer(t *testing.T) {
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	root := dir
	for i := 0; i < 8; i++ {
		if _, err := os.Stat(filepath.Join(root, "go.mod")); err == nil {
			break
		}
		root = filepath.Dir(root)
	}
	src, err := os.ReadFile(filepath.Join(root, "internal/inbox/digest.go"))
	if err != nil {
		t.Fatalf("read producer: %v", err)
	}
	if !strings.Contains(string(src), `DigestSubkind = "`+SubkindDigest+`"`) {
		t.Fatalf("internal/inbox/digest.go's DigestSubkind no longer matches %q — the category mapping keys off it", SubkindDigest)
	}
}

// TestCategoryForItem_RoutesDigestToRoutinesCompleted pins the digest's
// category mapping directly (as opposed to the drift-only test above).
func TestCategoryForItem_RoutesDigestToRoutinesCompleted(t *testing.T) {
	digest := map[string]interface{}{"subkind": SubkindDigest, "succeeded": 3}
	if got := CategoryForItem("message", digest); got != CategoryRoutinesCompleted {
		t.Errorf("digest → %q, want %q", got, CategoryRoutinesCompleted)
	}
}
