package documents

import (
	"io"
	"strings"
	"testing"
)

type secretCountingReader struct {
	io.Reader
	bytes int
}

func (reader *secretCountingReader) Read(buffer []byte) (int, error) {
	count, err := reader.Reader.Read(buffer)
	reader.bytes += count
	return count, err
}

// Content admission is platform-neutral even though the service file is Unix-only.
func TestServiceSecretContentBounds(t *testing.T) {
	for _, test := range []struct {
		name, input string
		valid       bool
	}{
		{"minimum", strings.Repeat("a", 32), true},
		{"maximum", strings.Repeat("a", 256), true},
		{"newline", strings.Repeat("a", 32) + "\n", true},
		{"short", strings.Repeat("a", 31), false},
		{"long", strings.Repeat("a", 257), false},
		{"file growth", strings.Repeat(" ", 513) + strings.Repeat("a", 32), false},
	} {
		t.Run(test.name, func(t *testing.T) {
			reader := &secretCountingReader{Reader: strings.NewReader(test.input)}
			_, err := readServiceSecretContents(reader)
			if (err == nil) != test.valid {
				t.Fatal("unexpected content admission result")
			}
			if reader.bytes > 513 {
				t.Fatal("content read exceeded bounded buffer")
			}
		})
	}
}
