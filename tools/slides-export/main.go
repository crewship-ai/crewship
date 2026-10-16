// slides-export qualifies a data-only converter. It is not a server API or a
// source of authorization: callers supply an already authorized saved snapshot.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"time"

	"github.com/crewship-ai/crewship/internal/pageassets"
	"github.com/crewship-ai/crewship/internal/slides"
)

const maxEnvelopeBytes = 64 << 20
const maxJSONDepth = 32
const maxJSONValues = 100000
const maxOutputBytes = 64 << 20
const maxReportBytes = 1 << 20
const maxAssetBytes = 32 << 20
const maxAssetPixels = 33554432
const processTimeout = 120 * time.Second

var assetID = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,128}$`)

type exportAsset struct {
	AssetID        string `json:"asset_id"`
	ExpectedSHA256 string `json:"expected_sha256"`
	MIME           string `json:"mime"`
	Width          int    `json:"width"`
	Height         int    `json:"height"`
	Data           string `json:"data"`
}
type exportInput struct {
	SchemaVersion int           `json:"schema_version"`
	Revision      int64         `json:"revision"`
	ContentHash   string        `json:"content_hash"`
	Deck          slides.Deck   `json:"deck"`
	Assets        []exportAsset `json:"assets"`
}

func exactObject(raw json.RawMessage, names ...string) (map[string]json.RawMessage, error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil || obj == nil || len(obj) != len(names) {
		return nil, errors.New("exact export object fields required")
	}
	for _, name := range names {
		if value, ok := obj[name]; !ok || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return nil, fmt.Errorf("export field %s required", name)
		}
	}
	return obj, nil
}
func rejectDuplicateKeys(data []byte) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	values := 0
	var walk func(int) error
	walk = func(depth int) error {
		values++
		if depth > maxJSONDepth || values > maxJSONValues {
			return errors.New("export JSON depth/value budget exceeded")
		}
		token, e := d.Token()
		if e != nil {
			return e
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for d.More() {
				key, e := d.Token()
				if e != nil {
					return e
				}
				name, ok := key.(string)
				if !ok || seen[name] {
					return errors.New("duplicate JSON export field")
				}
				seen[name] = true
				if e = walk(depth + 1); e != nil {
					return e
				}
			}
		case '[':
			for d.More() {
				if e = walk(depth + 1); e != nil {
					return e
				}
			}
		default:
			return errors.New("invalid JSON export delimiter")
		}
		_, e = d.Token()
		return e
	}
	if e := walk(0); e != nil {
		return e
	}
	if _, e := d.Token(); e != io.EOF {
		return errors.New("trailing export JSON")
	}
	return nil
}
func decodeExportInput(data []byte) (exportInput, error) {
	var out exportInput
	if len(data) == 0 || len(data) > maxEnvelopeBytes {
		return out, errors.New("export envelope exceeds64MiB")
	}
	if err := rejectDuplicateKeys(data); err != nil {
		return out, err
	}
	object, err := exactObject(data, "schema_version", "revision", "content_hash", "deck", "assets")
	if err != nil {
		return out, err
	}
	if err = json.Unmarshal(object["schema_version"], &out.SchemaVersion); err != nil || out.SchemaVersion != 1 {
		return out, errors.New("export protocol1 required")
	}
	if err = json.Unmarshal(object["revision"], &out.Revision); err != nil || out.Revision < 1 {
		return out, errors.New("explicit retained revision required")
	}
	if err = json.Unmarshal(object["content_hash"], &out.ContentHash); err != nil {
		return out, err
	}
	out.Deck, err = slides.Decode(object["deck"])
	if err != nil {
		return out, err
	}
	actual, err := slides.DeckHash(out.Deck)
	if err != nil || actual != out.ContentHash {
		return out, errors.New("saved canonical content hash mismatch")
	}
	var rawAssets []json.RawMessage
	if err = json.Unmarshal(object["assets"], &rawAssets); err != nil || rawAssets == nil || len(rawAssets) > 32 {
		return out, errors.New("bounded explicit asset array required")
	}
	expected := slides.ImageReferences(out.Deck)
	seen := map[string]bool{}
	out.Assets = []exportAsset{}
	var totalBytes, totalPixels int64
	for _, raw := range rawAssets {
		if _, err = exactObject(raw, "asset_id", "expected_sha256", "mime", "width", "height", "data"); err != nil {
			return out, err
		}
		var a exportAsset
		if err = json.Unmarshal(raw, &a); err != nil {
			return out, err
		}
		if !assetID.MatchString(a.AssetID) || seen[a.AssetID] || expected[a.AssetID] != a.ExpectedSHA256 || len(a.Data) > ((pageassets.MaxOutput+2)/3)*4 {
			return out, errors.New("unexpected/duplicate/oversized export asset")
		}
		decoded, e := base64.StdEncoding.DecodeString(a.Data)
		if e != nil || base64.StdEncoding.EncodeToString(decoded) != a.Data {
			return out, errors.New("canonical base64 asset required")
		}
		digest := sha256.Sum256(decoded)
		if hex.EncodeToString(digest[:]) != a.ExpectedSHA256 {
			return out, errors.New("private asset digest mismatch")
		}
		raster, e := pageassets.ValidateCanonical(decoded)
		if e != nil {
			return out, e
		}
		if raster.MIME != a.MIME || raster.Width != a.Width || raster.Height != a.Height {
			return out, errors.New("private asset metadata mismatch")
		}
		totalBytes += int64(len(decoded))
		totalPixels += int64(a.Width) * int64(a.Height)
		if totalBytes > maxAssetBytes || totalPixels > maxAssetPixels {
			return out, errors.New("aggregate image bytes/pixels exceeded")
		}
		seen[a.AssetID] = true
		out.Assets = append(out.Assets, a)
	}
	if len(seen) != len(expected) {
		return out, errors.New("required pinned image bytes missing")
	}
	// A single asset identity cannot name two digests at different placements.
	for _, slide := range out.Deck.Slides {
		for _, e := range slide.Elements {
			if e.Image != nil && expected[e.Image.AssetID] != e.Image.ExpectedSHA256 {
				return out, errors.New("conflicting image identity/digest")
			}
		}
	}
	return out, nil
}

type boundedOutput struct {
	// Keep the buffer private: embedding promotes ReadFrom and lets io.Copy
	// bypass Write, defeating the actual subprocess output budget.
	buffer bytes.Buffer
	limit  int
}

func (w *boundedOutput) Len() int       { return w.buffer.Len() }
func (w *boundedOutput) String() string { return w.buffer.String() }

func (w *boundedOutput) Write(data []byte) (int, error) {
	if len(data) > w.limit-w.Len() {
		return 0, errors.New("bounded worker output exceeded")
	}
	return w.buffer.Write(data)
}
func run(inputPath, outputDir string) error {
	file, err := os.Open(inputPath)
	if err != nil {
		return err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxEnvelopeBytes+1))
	if err != nil {
		return err
	}
	input, err := decodeExportInput(data)
	if err != nil {
		return err
	}
	if err = os.Mkdir(outputDir, 0700); err != nil {
		return fmt.Errorf("fresh output directory required: %w", err)
	}
	canonical, err := json.Marshal(input)
	if err != nil {
		return err
	}
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		return errors.New("converter source location unavailable")
	}
	worker := filepath.Join(filepath.Dir(source), "worker.mjs")
	ctx, cancel := context.WithTimeout(context.Background(), processTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", "--max-old-space-size=512", worker, "--output", outputDir)
	cmd.Stdin = bytes.NewReader(canonical)
	stdout, stderr := &boundedOutput{limit: maxReportBytes}, &boundedOutput{limit: 64 << 10}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	err = cmd.Run()
	if ctx.Err() != nil {
		return errors.New("export process120s timeout exceeded")
	}
	if err != nil {
		return fmt.Errorf("converter failed (report retained when available): %w; %s", err, stderr.String())
	}
	for _, name := range []string{"deck.pptx", "deck.pdf", "report.json"} {
		info, e := os.Stat(filepath.Join(outputDir, name))
		limit := int64(maxOutputBytes)
		if name == "report.json" {
			limit = maxReportBytes
		}
		if e != nil || !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > limit {
			return fmt.Errorf("invalid bounded %s output", name)
		}
	}
	_, err = fmt.Fprintln(os.Stdout, stdout.String())
	return err
}
func main() {
	corpusDir := flag.String("corpus", "", "write a fresh synthetic five-slide corpus directory")
	input := flag.String("input", "", "explicit saved export envelope JSON")
	output := flag.String("output", "", "fresh owned output directory")
	flag.Parse()
	if *corpusDir != "" {
		if err := writeCorpus(*corpusDir); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if *input == "" || *output == "" {
		fmt.Fprintln(os.Stderr, "--input and --output required")
		os.Exit(2)
	}
	if err := run(*input, *output); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
