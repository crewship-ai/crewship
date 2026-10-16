// slides-export qualifies a data-only converter. It is not a server API or a
// source of authorization: callers supply an already authorized saved snapshot.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"github.com/crewship-ai/crewship/internal/slidesexport"
)

const maxEnvelopeBytes = slidesexport.MaxInputBytes
const maxOutputBytes = slidesexport.MaxBinaryBytes
const maxReportBytes = slidesexport.MaxReportBytes
const processTimeout = 120 * time.Second

type exportInput = slidesexport.Input
type exportAsset = slidesexport.Asset

func decodeExportInput(data []byte) (exportInput, error) { return slidesexport.Decode(data) }
func rejectDuplicateKeys(data []byte) error              { return slidesexport.ValidateInputJSONStructure(data) }

const maxJSONValues = 100000

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
	if _, err = slidesexport.ProfileFingerprint(); err != nil {
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
	worker := filepath.Join(filepath.Dir(source), "runtime", "worker.mjs")
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
