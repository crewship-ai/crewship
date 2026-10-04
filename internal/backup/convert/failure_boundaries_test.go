package convert

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filippo.io/age"

	"github.com/crewship-ai/crewship/internal/backup"
)

type refusingRecipient struct{}

func (refusingRecipient) Wrap([]byte) ([]*age.Stanza, error) {
	return nil, errors.New("recipient refused sealing")
}

func TestConvertSealingFailureJoinsRewriterAndRemovesTemporaryFiles(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source.tar.zst")
	output := filepath.Join(dir, "converted.tar.zst")
	writeBundle(t, source, manifestAt(backup.FormatVersion), payloadOf(t, entry{"workspace/eng/data.txt", "original data"}), backup.WriteBundleOptions{NoEncrypt: true})
	original := fileSHA(t, source)
	registry, err := NewRegistry(Step{From: backup.FormatVersion, Manifest: func(*backup.Manifest, *PayloadIndex, *StepReport) error { return nil }, Entry: func(name string) (string, bool) { return name, true }})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := Convert(context.Background(), Options{BundlePath: source, OutPath: output, Registry: registry, Target: backup.FormatVersion + 1, Recipients: []age.Recipient{refusingRecipient{}}})
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "recipient refused sealing") {
			t.Fatalf("sealing failure lost: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("conversion did not join its rewriter after encryption refused the payload")
	}
	if _, err := os.Stat(output); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed conversion published output: %v", err)
	}
	if got := fileSHA(t, source); got != original {
		t.Fatal("failed conversion modified its source")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "source.tar.zst" {
		t.Fatalf("failed conversion retained temporary output: %#v", entries)
	}
}
