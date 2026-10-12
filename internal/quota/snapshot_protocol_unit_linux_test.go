//go:build linux

package quota

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestSnapshotResponsePreservesBinaryLookaheadAndCompletionFrame(t *testing.T) {
	k := Key{"crew", "database", "data", 1}
	d := Descriptor{ID: k.id(), Key: k, Bytes: MinBytes, Mount: "/private/mount"}
	header, err := json.Marshal(Response{Namespace: "installation-a", Descriptor: d})
	if err != nil {
		t.Fatal(err)
	}
	trailer, err := json.Marshal(Response{Namespace: "installation-a"})
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte{0, '\n', '{', '}', 255, '\n'}
	wire := append(append(append(header, '\n'), payload...), append(trailer, '\n')...)
	reader := bufio.NewReader(bytes.NewReader(wire))
	got, err := snapshotResponse(reader, "installation-a")
	if err != nil || got.Descriptor != d {
		t.Fatalf("snapshot header: %+v %v", got, err)
	}
	var dst bytes.Buffer
	if _, err := io.CopyN(&dst, reader, int64(len(payload))); err != nil || !bytes.Equal(dst.Bytes(), payload) {
		t.Fatalf("JSON decoder consumed binary lookahead: %x %v", dst.Bytes(), err)
	}
	if _, err := snapshotResponse(reader, "installation-a"); err != nil {
		t.Fatalf("completion frame lost: %v", err)
	}
}

func TestSnapshotResponseRejectsForeignMalformedOrIncompleteFrames(t *testing.T) {
	for _, frame := range []string{
		`{"Namespace":"foreign"}` + "\n",
		`{"Namespace":"installation-a","Error":"operation failed"}` + "\n",
		`{"Namespace":"installation-a"}`,
		`not-json` + "\n",
		strings.Repeat(" ", 8193) + `{"Namespace":"installation-a"}` + "\n",
		"",
	} {
		reader := bufio.NewReaderSize(strings.NewReader(frame), 16384)
		if _, err := snapshotResponse(reader, "installation-a"); !errors.Is(err, ErrDenied) {
			t.Fatalf("untrusted snapshot frame admitted: %v", err)
		}
	}
}

func TestSnapshotClientDeniesInvalidInputsBeforeDial(t *testing.T) {
	c := Client{Socket: "/nonexistent/crewship-quota-test.sock", Namespace: "installation-a"}
	k := Key{"crew", "database", "data", 1}
	if c.SnapshotNamespace() != "installation-a" {
		t.Fatal("host-selected snapshot identity lost")
	}
	if err := c.Export(t.Context(), k, MinBytes, nil); !errors.Is(err, ErrDenied) {
		t.Fatalf("nil destination admitted: %v", err)
	}
	if _, err := c.Import(t.Context(), k, MinBytes, nil); !errors.Is(err, ErrDenied) {
		t.Fatalf("nil source admitted: %v", err)
	}
	for _, tc := range []struct {
		key  Key
		size int64
	}{
		{Key{"../foreign", "database", "data", 1}, MinBytes}, {k, MinBytes - 1},
	} {
		if err := c.Export(t.Context(), tc.key, tc.size, &bytes.Buffer{}); !errors.Is(err, ErrDenied) {
			t.Fatalf("invalid export admitted: %v", err)
		}
		if _, err := c.Import(t.Context(), tc.key, tc.size, strings.NewReader("")); !errors.Is(err, ErrDenied) {
			t.Fatalf("invalid import admitted: %v", err)
		}
	}
	if err := c.Export(t.Context(), k, MinBytes, &bytes.Buffer{}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("missing helper export did not fail closed: %v", err)
	}
	if _, err := c.Import(t.Context(), k, MinBytes, strings.NewReader("")); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("missing helper import did not fail closed: %v", err)
	}
	if err := (Client{}).Export(t.Context(), k, MinBytes, &bytes.Buffer{}); !errors.Is(err, ErrDenied) {
		t.Fatalf("unconfigured snapshot endpoint admitted: %v", err)
	}
}
