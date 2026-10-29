package mooring

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

var ErrNativeValidation = errors.New("native document failed pinned engine validation")

// NativeCLIValidator invokes a configured, qualified original application CLI.
// The hash identifies the exact binary; engine revision and editor bundle are
// separately pinned by NativeStore. Validation never replaces original bytes
// with the engine's normalized export. It runs outside the SQL writer.
type NativeCLIValidator struct {
	Application, BinaryPath, SHA256 string
	Timeout                         time.Duration
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
func (v NativeCLIValidator) Validate(ctx context.Context, raw []byte) error {
	if v.Application == "deckcraft" {
		return v.validateDeck(ctx, raw)
	}
	trimmed := bytes.TrimSpace(raw)
	if len(raw) == 0 || len(raw) > 32<<20 || len(trimmed) == 0 || trimmed[0] != '{' || !json.Valid(raw) {
		return ErrNativeValidation
	}
	// /proc/self/fd executes the descriptor we hashed, closing replacement races.
	// Other platforms need an equally qualified launcher before enabling this profile.
	if runtime.GOOS != "linux" || v.Application != "wordcraft" && v.Application != "gridcraft" || !filepath.IsAbs(v.BinaryPath) || len(v.SHA256) != 64 {
		return ErrNativeValidation
	}
	expected, err := hex.DecodeString(v.SHA256)
	if err != nil {
		return ErrNativeValidation
	}
	f, err := os.Open(v.BinaryPath)
	if err != nil {
		return ErrNativeValidation
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() == 0 || info.Size() > 512<<20 {
		return ErrNativeValidation
	}
	digest := sha256.New()
	if _, err = io.Copy(digest, f); err != nil || !bytes.Equal(digest.Sum(nil), expected) {
		return ErrNativeValidation
	}
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		return ErrNativeValidation
	}
	timeout := v.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	if timeout <= 0 || timeout > time.Minute {
		return ErrNativeValidation
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
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
	args := []string{"convert", input, output}
	cmd := exec.CommandContext(ctx, "/proc/self/fd/3", args...)
	cmd.ExtraFiles = []*os.File{f}
	cmd.Dir = dir
	cmd.Env = []string{"LANG=C.UTF-8", "HOME=" + dir, "TMPDIR=" + dir}
	cmd.WaitDelay = 100 * time.Millisecond
	stdout := &nativeEngineOutput{limit: 1 << 20}
	stderr := &nativeEngineOutput{limit: 64 << 10}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if err = cmd.Run(); err != nil {
		return ErrNativeValidation
	}
	info, err = os.Stat(output)
	if err != nil || !info.Mode().IsRegular() || info.Size() == 0 || info.Size() > 64<<20 {
		return ErrNativeValidation
	}
	expectedSuccess := "wrote " + output + "\n"
	if v.Application == "gridcraft" {
		expectedSuccess = "wrote " + output + " (" + strconv.FormatInt(info.Size(), 10) + " bytes)\n"
	}
	// Both qualified CLIs log a success line on stderr. Any additional warning
	// or diagnostic refuses publication, even when the process exits zero.
	if stdout.Len() != 0 || stderr.String() != expectedSuccess {
		return ErrNativeValidation
	}
	b, err := os.ReadFile(output)
	if err != nil || !json.Valid(b) || !preservesNativeModel(raw, b, v.Application) {
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
