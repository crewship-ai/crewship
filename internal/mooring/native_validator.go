package mooring

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/craftengine"
)

var ErrNativeValidation = errors.New("native document failed pinned engine validation")

// NativeCLIValidator runs the pinned document engine of the application on a
// candidate. The engine's SHA-256 identifies its exact bytes; engine revision
// and editor bundle are separately pinned by NativeStore. Validation never
// replaces original bytes with the engine's normalized export. It runs outside
// the SQL writer.
type NativeCLIValidator struct {
	Application string
	Engine      craftengine.Engine
	Timeout     time.Duration
	// Trusted, when set, vouches for bytes this server's own engine wrote with every value
	// recalculated (a snapshot of its session in automatic calculation, or an import). Only those
	// gridcraft documents are converted without recalculating (convert --saved-values); the
	// engine must have the option. Nil, or false: recalculated as always.
	Trusted func(raw []byte) bool
}

type nativeEngineOutput struct {
	bytes.Buffer
	limit int
}

func (b *nativeEngineOutput) Write(p []byte) (int, error) {
	if len(p) > b.limit-b.Len() {
		return 0, ErrNativeValidation
	}
	return b.Buffer.Write(p)
}

// Validation deadline: the engine opens the whole document, which takes seconds for a small
// one and, measured on a 235 000 cell workbook, 26 s on a quiet machine and up to 77 s under
// load. A fixed 60 s failed such a workbook on a slower host; the deadline grows with the
// document up to a clear ceiling.
const (
	validationBase    = 30 * time.Second
	validationPerMiB  = 8 * time.Second
	MaxValidationTime = 5 * time.Minute
)

// ValidationTimeout is the time a validation of a document of size bytes may take.
func ValidationTimeout(size int) time.Duration { return ValidationTimeoutFrom(validationBase, size) }

// ValidationTimeoutFrom is ValidationTimeout with a caller's own base for a small document.
func ValidationTimeoutFrom(base time.Duration, size int) time.Duration {
	if size < 0 {
		size = 0
	}
	d := base + time.Duration(size>>20)*validationPerMiB
	if d > MaxValidationTime {
		d = MaxValidationTime
	}
	return d
}

// Limits of one validation. The engine writes its export indented: measured 2.7 to 3.3 times
// the compact document, so a document at the 32 MiB input limit exports up to about 106 MiB.
// That temporary file has its own limit; it is compacted while it is read, and the compact
// model (what is compared with the candidate) has a separate limit. Each validation holds the
// candidate, the compact export and both decoded models in memory and up to 160 MiB on disk, so
// only a few run at once; the others wait.
const (
	MaxValidationOutputBytes = 128 << 20
	MaxValidatedCompactBytes = 48 << 20
	MaxConcurrentValidations = 2
)

var validationSlots = make(chan struct{}, MaxConcurrentValidations)

// compactJSON strips the whitespace between tokens of r (which must be JSON) into a buffer of at
// most limit bytes, without holding the indented text. json.Valid checks the result afterwards.
func compactJSON(r io.Reader, limit int) ([]byte, error) {
	in := bufio.NewReaderSize(r, 1<<20)
	out := bytes.NewBuffer(make([]byte, 0, 1<<20))
	inString, escaped := false, false
	for {
		c, err := in.ReadByte()
		if err == io.EOF {
			return out.Bytes(), nil
		}
		if err != nil {
			return nil, err
		}
		switch {
		case inString:
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
		case c == '"':
			inString = true
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			continue
		}
		if out.Len() >= limit {
			return nil, ErrNativeValidation
		}
		out.WriteByte(c)
	}
}

func (v NativeCLIValidator) Validate(ctx context.Context, raw []byte) error {
	trimmed := bytes.TrimSpace(raw)
	if len(raw) == 0 || len(raw) > 32<<20 || len(trimmed) == 0 || trimmed[0] != '{' || !json.Valid(raw) {
		return ErrNativeValidation
	}
	if v.Application != "wordcraft" && v.Application != "gridcraft" || v.Engine == nil || v.Engine.Application() != v.Application {
		return ErrNativeValidation
	}
	timeout := v.Timeout
	if timeout == 0 {
		timeout = ValidationTimeout(len(raw))
	}
	if timeout <= 0 || timeout > MaxValidationTime {
		return ErrNativeValidation
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	select {
	case validationSlots <- struct{}{}:
		defer func() { <-validationSlots }()
	case <-ctx.Done():
		return ErrNativeValidation
	}
	dir, err := os.MkdirTemp("", "craft-native-")
	if err != nil {
		return ErrNativeValidation
	}
	defer os.RemoveAll(dir)
	input := filepath.Join(dir, "candidate.json")
	output := filepath.Join(dir, "validated.json")
	if err = os.WriteFile(input, raw, 0600); err != nil {
		return ErrNativeValidation
	}
	root := v.Engine.Root(dir)
	seenOutput := root + "/validated.json"
	stdout := &nativeEngineOutput{limit: 1 << 20}
	stderr := &nativeEngineOutput{limit: 64 << 10}
	args := []string{"convert", root + "/candidate.json", seenOutput}
	if v.Application == "gridcraft" && v.Trusted != nil && v.Trusted(raw) {
		// The server's own engine computed these values; recalculating would give the same (and
		// the model comparison below still holds every authored value to the export).
		args = append(args, "--saved-values")
	}
	if err = v.Engine.Run(ctx, dir, args, stdout, stderr); err != nil {
		return ErrNativeValidation
	}
	info, err := os.Stat(output)
	if err != nil || !info.Mode().IsRegular() || info.Size() == 0 || info.Size() > MaxValidationOutputBytes {
		return ErrNativeValidation
	}
	expectedSuccess := "wrote " + seenOutput + "\n"
	if v.Application == "gridcraft" {
		expectedSuccess = "wrote " + seenOutput + " (" + strconv.FormatInt(info.Size(), 10) + " bytes)\n"
	}
	// Both qualified engines log a success line on stderr. Any additional
	// warning or diagnostic refuses publication, even when the run succeeds.
	if stdout.Len() != 0 || stderr.String() != expectedSuccess {
		return ErrNativeValidation
	}
	f, err := os.Open(output)
	if err != nil {
		return ErrNativeValidation
	}
	b, err := compactJSON(io.LimitReader(f, MaxValidationOutputBytes), MaxValidatedCompactBytes)
	_ = f.Close()
	_ = os.Remove(output)
	if err != nil || !json.Valid(b) {
		return ErrNativeValidation
	}
	if bytes.Equal(b, trimmed) {
		return nil // the export is the candidate itself
	}
	if !preservesNativeModel(raw, b, v.Application) {
		return ErrNativeValidation
	}
	return nil
}

// Engine import can silently ignore unknown fields. Require all original
// authored values to survive its native export; only added defaults are allowed.
// Numeric spelling (1 versus 1.0) is irrelevant, while int64 values stay exact.
func preservesNativeModel(before, after []byte, application string) bool {
	decode := func(raw []byte) (any, error) {
		var out any
		d := json.NewDecoder(bytes.NewReader(raw))
		d.UseNumber()
		err := d.Decode(&out)
		return out, err
	}
	a, err := decode(before)
	if err != nil {
		return false
	}
	b, err := decode(after)
	if err != nil {
		return false
	}
	// The qualified Word convert ABI calls file.save, which stamps these
	// metadata/view fields. Compare all other authored values, and persist the
	// ORIGINAL bytes including their original metadata, never this export.
	if application == "wordcraft" {
		left, lok := a.(map[string]any)
		right, rok := b.(map[string]any)
		if !lok || !rok {
			return false
		}
		lc, lok := left["core"].(map[string]any)
		rc, rok := right["core"].(map[string]any)
		if lok && rok {
			for _, key := range []string{"modified", "lastModifiedBy", "revision"} {
				delete(lc, key)
				delete(rc, key)
			}
			if created, ok := lc["created"].(string); ok && created == "" {
				delete(lc, "created")
				delete(rc, "created")
			}
		}
		ls, lok := left["settings"].(map[string]any)
		rs, rok := right["settings"].(map[string]any)
		if lok && rok {
			delete(ls, "zoom")
			delete(rs, "zoom")
		}
	}
	return nativeModelSubset(a, b, 0)
}
func nativeModelSubset(a, b any, depth int) bool {
	if depth > 256 {
		return false
	}
	switch x := a.(type) {
	case map[string]any:
		y, ok := b.(map[string]any)
		if !ok {
			return false
		}
		for key, value := range x {
			next, present := y[key]
			if !present || !nativeModelSubset(value, next, depth+1) {
				return false
			}
		}
		return true
	case []any:
		y, ok := b.([]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for i, value := range x {
			if !nativeModelSubset(value, y[i], depth+1) {
				return false
			}
		}
		return true
	case json.Number:
		y, ok := b.(json.Number)
		if !ok {
			return false
		}
		parse := func(s string) (*big.Rat, bool) {
			if len(s) > 128 {
				return nil, false
			}
			if i := strings.IndexAny(s, "eE"); i >= 0 {
				exponent, err := strconv.Atoi(s[i+1:])
				if err != nil || exponent > 4096 || exponent < -4096 {
					return nil, false
				}
			}
			return new(big.Rat).SetString(s)
		}
		left, ok := parse(string(x))
		if !ok {
			return false
		}
		right, ok := parse(string(y))
		return ok && left.Cmp(right) == 0
	case string:
		y, ok := b.(string)
		return ok && x == y
	case bool:
		y, ok := b.(bool)
		return ok && x == y
	case nil:
		return b == nil
	}
	return false
}
