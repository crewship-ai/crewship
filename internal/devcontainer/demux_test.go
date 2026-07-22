package devcontainer

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/moby/moby/api/pkg/stdcopy"
)

// framed wraps s in a single stdcopy stdout frame, exactly as a non-TTY Docker
// exec attach would emit it. stdcopy.NewStdWriter was removed from the new
// github.com/moby/moby/api/pkg/stdcopy package (only StdCopy — the reader
// side — remains), so the frame header is built by hand here: it's a fixed,
// documented wire format ([stream type, 0, 0, 0, big-endian uint32 size]) that
// stdcopy.StdCopy on the other end still parses identically.
func framed(s string) []byte {
	var buf bytes.Buffer
	header := make([]byte, 8)
	header[0] = byte(stdcopy.Stdout)
	binary.BigEndian.PutUint32(header[4:], uint32(len(s)))
	buf.Write(header)
	buf.WriteString(s)
	return buf.Bytes()
}

func TestDemuxDockerStream_Framed(t *testing.T) {
	want := "/home/agent/.local/bin:/usr/local/bin:/usr/bin:/bin"
	got := string(demuxDockerStream(framed(want)))
	if got != want {
		t.Fatalf("demux of framed stream = %q, want %q", got, want)
	}
}

func TestDemuxDockerStream_RawPassthrough(t *testing.T) {
	// A raw (TTY / test-double) stream starts with printable bytes and must be
	// returned untouched — demuxing it would corrupt or drop the output.
	raw := "/usr/bin:/bin\nhook output line"
	if got := string(demuxDockerStream([]byte(raw))); got != raw {
		t.Fatalf("raw stream altered: got %q, want %q", got, raw)
	}
}

func TestLooksMultiplexed(t *testing.T) {
	if !looksMultiplexed(framed("x")) {
		t.Error("framed stream should be detected as multiplexed")
	}
	if looksMultiplexed([]byte("/usr/bin:/bin")) {
		t.Error("plain shell output must not be detected as multiplexed")
	}
	if looksMultiplexed([]byte{1, 0, 0}) {
		t.Error("too-short buffer must not be treated as framed")
	}
}
