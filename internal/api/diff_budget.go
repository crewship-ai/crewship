package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/pmezard/go-difflib/difflib"

	"github.com/crewship-ai/crewship/internal/config"
)

var errDiffTooLarge = errors.New("diff is too large")

// A single budget is shared by both handlers registered on one Router.
// Admission has no work queue; it stays held until synchronous work finishes.
type diffComputeBudget struct {
	limits config.DiffConfig
	active chan struct{}
	// Optional handler-owned seam, set before serving by boundary tests.
	// Production uses difflib.WriteUnifiedDiff. Never mutated during requests.
	compute func(io.Writer, difflib.UnifiedDiff) error
}

func newDiffComputeBudget(limits config.DiffConfig) *diffComputeBudget {
	return &diffComputeBudget{limits: limits, active: make(chan struct{}, limits.MaxConcurrent)}
}

func defaultDiffComputeBudget() *diffComputeBudget {
	limits, _ := (config.DiffConfig{}).Resolve()
	return newDiffComputeBudget(limits)
}

func (b *diffComputeBudget) acquire() (func(), bool) {
	select {
	case b.active <- struct{}{}:
		return func() { <-b.active }, true
	default:
		return nil, false
	}
}

func replyDiffBusy(w http.ResponseWriter) {
	w.Header().Set("Retry-After", "1")
	replyError(w, http.StatusTooManyRequests, "diff service is busy; retry shortly")
}

func replyDiffTooLarge(w http.ResponseWriter) {
	replyError(w, http.StatusRequestEntityTooLarge, "diff is too large; view the inputs separately or ask an administrator to adjust the diff limits")
}

func (b *diffComputeBudget) checkLengths(lengths ...int) error {
	remaining := b.limits.MaxInputBytes
	for _, n := range lengths {
		if n < 0 || n > remaining {
			return errDiffTooLarge
		}
		remaining -= n
	}
	return nil
}

func (b *diffComputeBudget) checkText(a, c string) error {
	if err := b.checkLengths(len(a), len(c)); err != nil {
		return err
	}
	// SplitLines deliberately keeps an additional final element, including
	// when the input ends in newline. Count exactly the matcher input lines.
	if strings.Count(a, "\n") >= b.limits.MaxLines || strings.Count(c, "\n") >= b.limits.MaxLines {
		return errDiffTooLarge
	}
	return nil
}

// prettyPair preserves prettyJSONOrRaw's exact encoding. Check the compact
// representation's exact indentation size BEFORE Encoder allocates it: deeply
// nested small JSON otherwise expands quadratically just from indentation.
func (b *diffComputeBudget) prettyPair(a, c string) (string, string, error) {
	if err := b.checkLengths(len(a), len(c)); err != nil {
		return "", "", err
	}
	first, err := b.pretty(a, b.limits.MaxInputBytes)
	if err != nil {
		return "", "", err
	}
	second, err := b.pretty(c, b.limits.MaxInputBytes-len(first))
	if err != nil {
		return "", "", err
	}
	return first, second, nil
}

func (b *diffComputeBudget) pretty(raw string, maxBytes int) (string, error) {
	var value any
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		if len(raw) > maxBytes || strings.Count(raw, "\n") >= b.limits.MaxLines {
			return "", errDiffTooLarge
		}
		return raw, nil
	}
	compact, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	if err := checkDiffJSONIndent(compact, maxBytes, b.limits.MaxLines); err != nil {
		return "", err
	}
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		return "", err
	}
	return out.String(), nil
}

// compact is valid JSON emitted by json.Marshal. Each structural newline and
// its two-space indentation is counted without materializing indentation.
func checkDiffJSONIndent(compact []byte, maxBytes, maxLines int) error {
	if len(compact) >= maxBytes || maxLines < 2 {
		return errDiffTooLarge
	}
	size := len(compact) + 1 // Encoder.Encode's final newline.
	lines := 2               // SplitLines has newline count plus one elements.
	depth := 0
	quoted := false
	escaped := false
	add := func(n, newLines int) bool {
		if n > maxBytes-size || newLines > maxLines-lines {
			return false
		}
		size += n
		lines += newLines
		return true
	}
	for i := 0; i < len(compact); i++ {
		c := compact[i]
		if quoted {
			if escaped {
				escaped = false
				continue
			}
			if c == '\\' {
				escaped = true
			} else if c == '"' {
				quoted = false
			}
			continue
		}
		switch c {
		case '"':
			quoted = true
		case '{', '[':
			close := byte('}')
			if c == '[' {
				close = ']'
			}
			if i+1 < len(compact) && compact[i+1] == close {
				i++
				continue
			}
			depth++
			if !add(1+2*depth, 1) {
				return errDiffTooLarge
			}
		case '}', ']':
			depth--
			if depth < 0 || !add(1+2*depth, 1) {
				return errDiffTooLarge
			}
		case ',':
			if !add(1+2*depth, 1) {
				return errDiffTooLarge
			}
		case ':':
			if !add(1, 0) {
				return errDiffTooLarge
			}
		}
	}
	return nil
}

type diffOutputWriter struct {
	buffer bytes.Buffer
	limit  int
	err    error
}

func (w *diffOutputWriter) Write(p []byte) (int, error) {
	if w.err != nil {
		return 0, w.err
	}
	if len(p) > w.limit-w.buffer.Len() {
		w.err = errDiffTooLarge
		return 0, w.err
	}
	return w.buffer.Write(p)
}

func (b *diffComputeBudget) render(a, c, from, to string) (string, error) {
	if err := b.checkText(a, c); err != nil {
		return "", err
	}
	out := &diffOutputWriter{limit: 4*b.limits.MaxInputBytes + (128 << 10)}
	compute := b.compute
	if compute == nil {
		compute = difflib.WriteUnifiedDiff
	}
	err := compute(out, difflib.UnifiedDiff{A: difflib.SplitLines(a), B: difflib.SplitLines(c), FromFile: from, ToFile: to, Context: 3})
	if err != nil {
		return "", err
	}
	// The library defers its bufio.Flush and discards that error. Keep the
	// underlying writer's sticky error so the final flush cannot become 200.
	if out.err != nil {
		return "", out.err
	}
	return out.buffer.String(), nil
}

type diffContextReader struct {
	ctx    context.Context
	source io.Reader
}

func (r diffContextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.source.Read(p)
}

func (b *diffComputeBudget) readFile(ctx context.Context, file *os.File) ([]byte, bool, error) {
	data, err := io.ReadAll(io.LimitReader(diffContextReader{ctx: ctx, source: file}, int64(min(b.limits.MaxInputBytes, proposalDiffMaxBytes))+1))
	if err != nil {
		return nil, false, err
	}
	if len(data) > min(b.limits.MaxInputBytes, proposalDiffMaxBytes) {
		return nil, true, nil
	}
	return data, false, nil
}

// WithDiffLimits configures the shared preview budget before route registration.
func WithDiffLimits(limits config.DiffConfig) RouterOption {
	return func(r *Router) { r.diffLimits = limits }
}
