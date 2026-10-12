package scrubber

import (
	"encoding/base64"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestStreamScrubberEmitsLongLogsWithoutCredentialFragments(t *testing.T) {
	key := "sk-ant-" + strings.Repeat("sample", 7)
	input := strings.Repeat("ordinary log entry\n", 14) + key + "\n" + strings.Repeat("finished task\n", 30)
	want := New().Scrub(input)
	for _, chunkSize := range []int{1, 7, 31, 127, 128, 129, 211, len(input)} {
		t.Run(fmt.Sprint(chunkSize), func(t *testing.T) {
			stream := NewStreamScrubber(nil)
			var output strings.Builder
			for offset := 0; offset < len(input); offset += chunkSize {
				end := min(offset+chunkSize, len(input))
				output.WriteString(stream.Write(input[offset:end]))
			}
			if output.Len() == 0 {
				t.Fatal("long log was buffered until end of stream")
			}
			output.WriteString(stream.Flush())
			if output.String() != want {
				t.Fatalf("stream differs from complete redaction: %q", output.String())
			}
			if stream.Flush() != "" {
				t.Fatal("second flush duplicated output")
			}
		})
	}
}

func TestStreamScrubberPreservesUTF8AcrossEveryByteBoundary(t *testing.T) {
	input := strings.Repeat("Příliš žluťoučký 🦜 日本語\n", 24)
	stream := NewStreamScrubber(nil)
	var output strings.Builder
	for i := 0; i < len(input); i++ {
		emitted := stream.Write(input[i : i+1])
		if !utf8.ValidString(emitted) {
			t.Fatalf("invalid UTF-8 emitted at byte %d", i)
		}
		output.WriteString(emitted)
	}
	output.WriteString(stream.Flush())
	if output.String() != input {
		t.Fatal("Unicode log content was lost or reordered")
	}
}

func TestStreamScrubberKeepsLongEncodedValuesTogether(t *testing.T) {
	secret := strings.Repeat("fixture-", 30) + "終"
	encoded := base64.StdEncoding.EncodeToString([]byte(secret))
	stream := NewStreamScrubber(nil)
	stream.AddSecretValues(secret, secret, "tiny")
	input := strings.Repeat("prefix ", 130) + encoded + strings.Repeat(" suffix", 140)
	var output strings.Builder
	for offset := 0; offset < len(input); offset += 97 {
		output.WriteString(stream.Write(input[offset:min(offset+97, len(input))]))
	}
	output.WriteString(stream.Flush())
	want := strings.Repeat("prefix ", 130) + "[REDACTED:secret-value]" + strings.Repeat(" suffix", 140)
	if output.String() != want {
		t.Fatal("encoded value leaked or surrounding text was lost")
	}
}

func TestUnknownValidationModeFailsClosedOnlyForCredentialHits(t *testing.T) {
	s := New()
	if err := s.AddPattern("fixture", `sensitive-example`); err != nil {
		t.Fatal(err)
	}
	for _, input := range []string{"ordinary text", "sensitive-example"} {
		got := s.Validate(input, Mode(999))
		want := DecisionAllow
		if input == "sensitive-example" {
			want = DecisionReject
		}
		if got.Decision != want || got.Cleaned != input {
			t.Fatalf("unknown mode result: %+v", got)
		}
	}
	if err := s.AddPattern("", `custom-example`); err != nil {
		t.Fatal(err)
	}
	if got := s.Scrub("before custom-example after"); got != "before [REDACTED] after" {
		t.Fatalf("unnamed pattern = %q", got)
	}
}
