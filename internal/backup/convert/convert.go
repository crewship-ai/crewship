package convert

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"filippo.io/age"
	"github.com/klauspost/compress/zstd"

	"github.com/crewship-ai/crewship/internal/backup"
)

// Outer-bundle member names, mirrored from internal/backup/bundle.go. The
// converter reads the outer tar itself because backup.ReadBundle refuses
// bundles outside the direct-read window — which are exactly the ones
// that most need converting.
const (
	manifestMember = "MANIFEST.json"
	payloadMember  = "payload"
	dumpEntry      = "db/dump.json"

	maxManifestBytes int64 = 4 << 20   // same bound as backup.maxBackupManifestBytes
	maxDumpBytes     int64 = 500 << 20 // same bound as backup.maxBackupDBDumpBytes
)

var (
	// ErrNoConverter means the chain from the bundle's version to the
	// current one has a gap. It can only happen if a FormatVersion bump
	// landed without its step, which TestConverterChainCoversEveryVersion
	// exists to prevent.
	ErrNoConverter = errors.New("convert: no converter registered for this format version")
	// ErrOutputExists refuses to clobber an existing file at --out.
	ErrOutputExists = errors.New("convert: output file already exists")
	// ErrNoOutputKey means the converted bundle has nothing to be sealed
	// to: the source was unencrypted (legacy) and no recipient or
	// passphrase was supplied. New bundles are always encrypted.
	ErrNoOutputKey = errors.New("convert: no key to encrypt the converted bundle to")
)

// StepReport is what one converter step did.
type StepReport struct {
	From    int      `json:"from" yaml:"from"`
	To      int      `json:"to" yaml:"to"`
	Summary string   `json:"summary" yaml:"summary"`
	Changes []string `json:"changes" yaml:"changes"`
	// Unrecoverable names data the bundle's version never carried, so no
	// conversion can bring it back.
	Unrecoverable []string `json:"unrecoverable,omitempty" yaml:"unrecoverable,omitempty"`
	Warnings      []string `json:"warnings,omitempty" yaml:"warnings,omitempty"`
}

func (r *StepReport) change(format string, a ...any) {
	r.Changes = append(r.Changes, fmt.Sprintf(format, a...))
}

func (r *StepReport) unrecoverable(format string, a ...any) {
	r.Unrecoverable = append(r.Unrecoverable, fmt.Sprintf(format, a...))
}

func (r *StepReport) warn(format string, a ...any) {
	r.Warnings = append(r.Warnings, fmt.Sprintf(format, a...))
}

// Report is the result of Convert. Output is empty when nothing was
// written (the bundle was already at the current version).
type Report struct {
	Source string       `json:"source" yaml:"source"`
	Output string       `json:"output,omitempty" yaml:"output,omitempty"`
	From   int          `json:"from" yaml:"from"`
	To     int          `json:"to" yaml:"to"`
	Steps  []StepReport `json:"steps" yaml:"steps"`
	// Unrecoverable is every step's Unrecoverable, in order.
	Unrecoverable []string `json:"unrecoverable,omitempty" yaml:"unrecoverable,omitempty"`
	// Warnings covers the bundle as a whole (and every step's warnings).
	Warnings []string `json:"warnings,omitempty" yaml:"warnings,omitempty"`
	// Encryption describes how the converted bundle is sealed.
	Encryption          string `json:"encryption,omitempty" yaml:"encryption,omitempty"`
	SourcePayloadSHA256 string `json:"source_payload_sha256" yaml:"source_payload_sha256"`
	PayloadSHA256       string `json:"payload_sha256,omitempty" yaml:"payload_sha256,omitempty"`
	SizeBytes           int64  `json:"size_bytes,omitempty" yaml:"size_bytes,omitempty"`
}

// PayloadIndex is what the first (read-only) pass learned about the
// decrypted payload. Steps read it to set manifest flags from what the
// bundle really contains instead of from what its manifest claims.
type PayloadIndex struct {
	// HasDBDump reports whether db/dump.json is present; Tables maps each
	// table in it to its row count.
	HasDBDump bool
	Tables    map[string]int
	// SectionFiles counts regular files per top-level per-crew section
	// ("workspace", "memory", "crew", "volumes", "system") and crew slug.
	SectionFiles map[string]map[string]int
	// CrewMemoryFiles counts, per crew slug, regular files inside a
	// `.memory` directory of the crew/<slug>/ section.
	CrewMemoryFiles map[string]int
	// MemoryBlobs counts files in the memory-blobs/ section.
	MemoryBlobs int
	Entries     int
}

func (p *PayloadIndex) sectionFiles(section, slug string) int {
	return p.SectionFiles[section][slug]
}

// Step converts a bundle from From to From+1. Manifest is required; Dump
// and Entry are optional payload rewrites — when any step in a chain sets
// one, the payload is re-packed entry by entry, otherwise its decrypted
// bytes pass through unchanged.
type Step struct {
	From    int
	Summary string
	// Manifest rewrites m (a private copy) to describe the payload at
	// From+1, recording what it did in r.
	Manifest func(m *backup.Manifest, idx *PayloadIndex, r *StepReport) error
	// Dump rewrites the DB dump. Numbers arrive as json.Number.
	Dump func(d *backup.DBDump, r *StepReport) error
	// Entry renames (or, with keep=false, drops) a payload entry.
	Entry func(name string) (newName string, keep bool)
}

func (s Step) rewritesPayload() bool { return s.Dump != nil || s.Entry != nil }

// Registry holds one Step per source version.
type Registry struct {
	steps map[int]Step
}

// NewRegistry builds a registry, refusing duplicate or malformed steps.
func NewRegistry(steps ...Step) (*Registry, error) {
	r := &Registry{steps: map[int]Step{}}
	for _, s := range steps {
		if s.From < 1 {
			return nil, fmt.Errorf("convert: step from v%d: version must be positive", s.From)
		}
		if s.Manifest == nil {
			return nil, fmt.Errorf("convert: step v%d→v%d has no Manifest func", s.From, s.From+1)
		}
		if _, dup := r.steps[s.From]; dup {
			return nil, fmt.Errorf("convert: two steps from v%d", s.From)
		}
		r.steps[s.From] = s
	}
	return r, nil
}

// Step returns the step that converts from the given version.
func (r *Registry) Step(from int) (Step, bool) {
	s, ok := r.steps[from]
	return s, ok
}

// Chain returns the steps from `from` up to `to`, in order.
func (r *Registry) Chain(from, to int) ([]Step, error) {
	var out []Step
	for v := from; v < to; v++ {
		s, ok := r.steps[v]
		if !ok {
			return nil, fmt.Errorf("%w: v%d→v%d", ErrNoConverter, v, v+1)
		}
		out = append(out, s)
	}
	return out, nil
}

// Options configures Convert.
type Options struct {
	BundlePath string
	OutPath    string

	// Keys that open the source bundle.
	Identities []age.Identity
	Passphrase string

	// Output sealing. Both empty: reuse the source's recipients (from its
	// manifest) or the passphrase that opened it.
	Recipients    []age.Recipient
	OutPassphrase string

	// Registry defaults to Default(). Target defaults to
	// backup.FormatVersion; tests use it to exercise synthetic chains.
	Registry *Registry
	Target   int

	ConverterVersion string
	Now              func() time.Time
}

// Convert writes a copy of opts.BundlePath converted to the current
// FormatVersion at opts.OutPath and returns the report. The source is
// only read. A bundle already at the target version is reported with no
// steps and nothing is written.
func Convert(ctx context.Context, opts Options) (*Report, error) {
	if opts.BundlePath == "" {
		return nil, errors.New("convert: bundle path required")
	}
	reg := opts.Registry
	if reg == nil {
		reg = Default()
	}
	target := opts.Target
	if target == 0 {
		target = backup.FormatVersion
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}

	// Pass 1: verify the checksum and index the payload.
	src, err := openBundle(opts.BundlePath)
	if err != nil {
		return nil, err
	}
	manifest := src.manifest
	report := &Report{
		Source:              opts.BundlePath,
		From:                manifest.FormatVersion,
		To:                  target,
		Steps:               []StepReport{},
		SourcePayloadSHA256: manifest.Checksums.PayloadSHA256,
	}
	if manifest.FormatVersion > target {
		_ = src.Close()
		return nil, fmt.Errorf("%w: bundle is v%d, this converter targets v%d", backup.ErrFormatTooNew, manifest.FormatVersion, target)
	}
	idx, err := indexPayload(src, opts)
	_ = src.Close()
	if err != nil {
		return nil, err
	}
	if manifest.FormatVersion == target {
		return report, nil
	}

	if opts.OutPath == "" {
		return nil, errors.New("convert: output path required")
	}
	if same, _ := samePath(opts.BundlePath, opts.OutPath); same {
		return nil, errors.New("convert: output must differ from the bundle; the original is never modified")
	}
	if _, err := os.Lstat(opts.OutPath); err == nil {
		return nil, fmt.Errorf("%w: %s", ErrOutputExists, opts.OutPath)
	}

	chain, err := reg.Chain(manifest.FormatVersion, target)
	if err != nil {
		return nil, err
	}
	out, err := copyManifest(manifest)
	if err != nil {
		return nil, err
	}
	rewrite := false
	for _, s := range chain {
		sr := StepReport{From: s.From, To: s.From + 1, Summary: s.Summary, Changes: []string{}}
		if err := s.Manifest(out, idx, &sr); err != nil {
			return nil, fmt.Errorf("convert: step v%d→v%d: %w", s.From, s.From+1, err)
		}
		out.FormatVersion = s.From + 1
		rewrite = rewrite || s.rewritesPayload()
		report.Steps = append(report.Steps, sr)
	}

	sealOpts, desc, err := outputSealing(manifest, opts)
	if err != nil {
		return nil, err
	}
	report.Encryption = desc

	// Pass 2: decrypt again, rewrite if a step asks to, seal to a temp
	// file next to the output (ciphertext only), then assemble.
	outDir := filepath.Dir(opts.OutPath)
	sealedTmp, err := os.CreateTemp(outDir, ".crewship-convert-sealed-*")
	if err != nil {
		return nil, fmt.Errorf("convert: temp file: %w", err)
	}
	defer func() { _ = sealedTmp.Close(); _ = os.Remove(sealedTmp.Name()) }()

	src2, err := openBundle(opts.BundlePath)
	if err != nil {
		return nil, err
	}
	defer func() { _ = src2.Close() }()
	plain, err := decryptPayload(src2.payload, manifest, opts)
	if err != nil {
		return nil, err
	}
	var inner io.Reader = plain
	var rewriteErr chan error
	var rewriteReader *io.PipeReader
	if rewrite {
		pr, pw := io.Pipe()
		rewriteReader = pr
		rewriteErr = make(chan error, 1)
		stepReports := report.Steps
		go func() {
			err := rewritePayload(ctx, plain, pw, chain, stepReports)
			_ = pw.CloseWithError(err)
			rewriteErr <- err
		}()
		inner = pr
	}
	sum, size, err := backup.SealPayload(sealedTmp, inner, sealOpts)
	if rewriteErr != nil {
		// Sealing can fail before consuming the rewrite stream (for example,
		// when a recipient refuses the key or the destination fills). Release
		// the producer before joining it; otherwise its pipe write can block
		// forever while this goroutine waits for the producer's result.
		_ = rewriteReader.CloseWithError(err)
		if rerr := <-rewriteErr; rerr != nil && err == nil {
			err = rerr
		}
	}
	if err != nil {
		return nil, fmt.Errorf("convert: seal payload: %w", err)
	}
	if _, err := sealedTmp.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}

	// After the rewrite goroutine has finished, so step warnings a Dump
	// rewrite recorded are included.
	bundleWarnings(out, idx, report)
	out.FormatVersion = target
	out.Encryption = encryptionFor(sealOpts)
	out.Checksums.PayloadSHA256 = sum
	unrec := []string{}
	for _, s := range report.Steps {
		unrec = append(unrec, s.Unrecoverable...)
	}
	report.Unrecoverable = unrec
	out.Conversion = &backup.Conversion{
		FromFormatVersion:   manifest.FormatVersion,
		ConvertedAt:         now().UTC(),
		ConverterVersion:    opts.ConverterVersion,
		SourcePayloadSHA256: manifest.Checksums.PayloadSHA256,
		Unrecoverable:       unrec,
	}

	outTmp, err := os.CreateTemp(outDir, ".crewship-convert-out-*")
	if err != nil {
		return nil, fmt.Errorf("convert: temp file: %w", err)
	}
	outTmpName := outTmp.Name()
	defer func() { _ = os.Remove(outTmpName) }()
	if err := backup.WriteBundleStream(outTmp, out, sealedTmp, size); err != nil {
		_ = outTmp.Close()
		return nil, fmt.Errorf("convert: write bundle: %w", err)
	}
	if err := outTmp.Sync(); err != nil {
		_ = outTmp.Close()
		return nil, err
	}
	info, err := outTmp.Stat()
	if err != nil {
		_ = outTmp.Close()
		return nil, err
	}
	if err := outTmp.Close(); err != nil {
		return nil, err
	}
	if err := os.Chmod(outTmpName, 0o600); err != nil {
		return nil, err
	}
	// Link, not rename: link refuses an existing destination atomically,
	// so a file that appeared at --out while we worked is not clobbered.
	if err := os.Link(outTmpName, opts.OutPath); err != nil {
		if errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("%w: %s", ErrOutputExists, opts.OutPath)
		}
		return nil, fmt.Errorf("convert: place output: %w", err)
	}
	report.Output = opts.OutPath
	report.PayloadSHA256 = sum
	report.SizeBytes = info.Size()
	return report, nil
}

// bundleWarnings adds findings that are about the bundle as a whole
// rather than one version step.
func bundleWarnings(m *backup.Manifest, idx *PayloadIndex, report *Report) {
	for _, s := range report.Steps {
		report.Warnings = append(report.Warnings, s.Warnings...)
	}
	if idx.HasDBDump && len(m.Contents.TableRowCounts) == 0 {
		report.Warnings = append(report.Warnings,
			"completeness cannot be verified: the bundle predates per-table row counts (#2009); the converter does not invent them from the payload it is checking")
	}
	if idx.Tables["memory_versions"] > 0 && idx.MemoryBlobs == 0 {
		report.Warnings = append(report.Warnings, fmt.Sprintf(
			"%d memory_versions row(s) but no memory-blobs section: this bundle predates blob collection (#1537) or its source had no blob store, so memory version content will not be readable after restore",
			idx.Tables["memory_versions"]))
	}
	if len(m.Contents.MissingContainerCrews) > 0 {
		report.Warnings = append(report.Warnings, "crews whose container was missing at backup time carry no files: "+strings.Join(m.Contents.MissingContainerCrews, ", "))
	}
}

// ---------------------------------------------------------------------
// Reading

type sourceBundle struct {
	f        *os.File
	outer    *backup.TarZstReader
	manifest *backup.Manifest
	payload  *backup.HashingReader
}

func (s *sourceBundle) Close() error {
	if s.outer != nil {
		_ = s.outer.Close()
	}
	return s.f.Close()
}

// openBundle positions a reader at the sealed payload without applying
// the direct-read compatibility gate.
func openBundle(path string) (*sourceBundle, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("convert: open bundle: %w", err)
	}
	outer, err := backup.NewTarZstReader(f)
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	s := &sourceBundle{f: f, outer: outer}
	for {
		hdr, err := outer.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			_ = s.Close()
			return nil, fmt.Errorf("convert: read bundle: %w", err)
		}
		switch hdr.Name {
		case manifestMember:
			data, err := io.ReadAll(io.LimitReader(outer, maxManifestBytes))
			if err != nil {
				_ = s.Close()
				return nil, fmt.Errorf("convert: read manifest: %w", err)
			}
			m, err := backup.ReadManifest(data)
			if err != nil {
				_ = s.Close()
				return nil, err
			}
			s.manifest = m
		case payloadMember:
			if s.manifest == nil {
				_ = s.Close()
				return nil, fmt.Errorf("%w: payload before MANIFEST.json", backup.ErrInvalidManifest)
			}
			s.payload = backup.NewHashingReader(outer)
			return s, nil
		}
	}
	_ = s.Close()
	if s.manifest == nil {
		return nil, fmt.Errorf("%w: MANIFEST.json missing from bundle", backup.ErrInvalidManifest)
	}
	return nil, fmt.Errorf("%w: payload missing from bundle", backup.ErrInvalidManifest)
}

func decryptPayload(sealed io.Reader, m *backup.Manifest, opts Options) (io.Reader, error) {
	if !m.Encryption.Enabled {
		return sealed, nil
	}
	switch {
	case opts.Passphrase != "":
		return backup.DecryptStreamPassphrase(sealed, opts.Passphrase)
	case len(opts.Identities) > 0:
		return backup.DecryptStream(sealed, opts.Identities...)
	default:
		return nil, errors.New("convert: bundle is encrypted; supply --identity or --passphrase-file")
	}
}

func indexPayload(src *sourceBundle, opts Options) (*PayloadIndex, error) {
	plain, err := decryptPayload(src.payload, src.manifest, opts)
	if err != nil {
		return nil, err
	}
	idx := &PayloadIndex{
		Tables:          map[string]int{},
		SectionFiles:    map[string]map[string]int{},
		CrewMemoryFiles: map[string]int{},
	}
	tr, err := backup.NewTarZstReader(plain)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tr.Close() }()
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("convert: read payload: %w", err)
		}
		idx.Entries++
		name := strings.TrimPrefix(hdr.Name, "./")
		if name == dumpEntry {
			var d struct {
				Tables map[string][]json.RawMessage `json:"tables"`
			}
			data, err := io.ReadAll(io.LimitReader(tr, maxDumpBytes))
			if err != nil {
				return nil, err
			}
			if err := json.Unmarshal(data, &d); err != nil {
				return nil, fmt.Errorf("convert: parse %s: %w", dumpEntry, err)
			}
			idx.HasDBDump = true
			for t, rows := range d.Tables {
				idx.Tables[t] = len(rows)
			}
			continue
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		parts := strings.Split(name, "/")
		switch parts[0] {
		case "memory-blobs":
			idx.MemoryBlobs++
		case "workspace", "memory", "crew", "volumes", "system":
			if len(parts) < 3 {
				continue
			}
			sec, slug := parts[0], parts[1]
			if idx.SectionFiles[sec] == nil {
				idx.SectionFiles[sec] = map[string]int{}
			}
			idx.SectionFiles[sec][slug]++
			if sec == "crew" {
				for _, p := range parts[2 : len(parts)-1] {
					if p == ".memory" {
						idx.CrewMemoryFiles[slug]++
						break
					}
				}
			}
		}
	}
	// Drain whatever follows the tar trailer, then check the digest: a
	// corrupted or tampered bundle is refused, never converted.
	if _, err := io.Copy(io.Discard, plain); err != nil {
		return nil, fmt.Errorf("convert: read payload: %w", err)
	}
	if _, err := io.Copy(io.Discard, src.payload); err != nil {
		return nil, fmt.Errorf("convert: read payload: %w", err)
	}
	if err := backup.VerifyChecksum(src.manifest.Checksums.PayloadSHA256, src.payload.Sum()); err != nil {
		return nil, err
	}
	return idx, nil
}

// ---------------------------------------------------------------------
// Writing

// rewritePayload re-packs the decrypted inner tar.zst entry by entry,
// applying every step's Entry and Dump rewrite in chain order.
func rewritePayload(ctx context.Context, plain io.Reader, w io.Writer, chain []Step, reports []StepReport) error {
	tr, err := backup.NewTarZstReader(plain)
	if err != nil {
		return err
	}
	defer func() { _ = tr.Close() }()
	zw, err := zstd.NewWriter(w)
	if err != nil {
		return err
	}
	tw := tar.NewWriter(zw)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("convert: read payload: %w", err)
		}
		name := strings.TrimPrefix(hdr.Name, "./")
		keep := true
		for _, s := range chain {
			if s.Entry != nil && keep {
				name, keep = s.Entry(name)
			}
		}
		if !keep {
			continue
		}
		out := *hdr
		out.Name = name
		if name == dumpEntry && hdr.Typeflag == tar.TypeReg {
			data, err := io.ReadAll(io.LimitReader(tr, maxDumpBytes))
			if err != nil {
				return err
			}
			data, err = rewriteDump(data, chain, reports)
			if err != nil {
				return err
			}
			out.Size = int64(len(data))
			if err := tw.WriteHeader(&out); err != nil {
				return err
			}
			if _, err := tw.Write(data); err != nil {
				return err
			}
			continue
		}
		if err := tw.WriteHeader(&out); err != nil {
			return err
		}
		if hdr.Typeflag == tar.TypeReg {
			if _, err := io.CopyN(tw, tr, hdr.Size); err != nil {
				return err
			}
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return zw.Close()
}

func rewriteDump(data []byte, chain []Step, reports []StepReport) ([]byte, error) {
	var d backup.DBDump
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(&d); err != nil {
		return nil, fmt.Errorf("convert: parse %s: %w", dumpEntry, err)
	}
	for i, s := range chain {
		if s.Dump == nil {
			continue
		}
		if err := s.Dump(&d, &reports[i]); err != nil {
			return nil, fmt.Errorf("convert: step v%d→v%d dump: %w", s.From, s.From+1, err)
		}
	}
	return json.Marshal(&d)
}

// outputSealing decides what the converted bundle is encrypted to.
func outputSealing(src *backup.Manifest, opts Options) (backup.WriteBundleOptions, string, error) {
	switch {
	case len(opts.Recipients) > 0:
		return backup.WriteBundleOptions{Recipients: opts.Recipients}, "recipients: " + joinRecipients(opts.Recipients) + " (as requested)", nil
	case opts.OutPassphrase != "":
		return backup.WriteBundleOptions{Passphrase: opts.OutPassphrase}, "passphrase (as requested)", nil
	}
	if !src.Encryption.Enabled {
		return backup.WriteBundleOptions{}, "", fmt.Errorf("%w: the source bundle is unencrypted; pass --recipient", ErrNoOutputKey)
	}
	if len(src.Encryption.Recipients) > 0 {
		var rs []age.Recipient
		for _, s := range src.Encryption.Recipients {
			r, err := age.ParseX25519Recipient(s)
			if err != nil {
				if opts.Passphrase != "" {
					// A recipient string we cannot re-use (e.g. an scrypt
					// marker): the passphrase that opened it seals it again.
					return backup.WriteBundleOptions{Passphrase: opts.Passphrase}, "passphrase (the one that opened the source)", nil
				}
				return backup.WriteBundleOptions{}, "", fmt.Errorf("%w: cannot re-encrypt to recorded recipient %q; pass --recipient", ErrNoOutputKey, s)
			}
			rs = append(rs, r)
		}
		return backup.WriteBundleOptions{Recipients: rs}, "recipients: " + joinRecipients(rs) + " (same as the source)", nil
	}
	if opts.Passphrase != "" {
		return backup.WriteBundleOptions{Passphrase: opts.Passphrase}, "passphrase (the one that opened the source)", nil
	}
	return backup.WriteBundleOptions{}, "", fmt.Errorf("%w: the source records no recipients and was not opened with a passphrase; pass --recipient", ErrNoOutputKey)
}

func encryptionFor(o backup.WriteBundleOptions) backup.Encryption {
	if len(o.Recipients) > 0 {
		e := backup.Encryption{Enabled: true, Algorithm: backup.EncryptionAlgorithm}
		for _, r := range o.Recipients {
			e.Recipients = append(e.Recipients, recipientString(r))
		}
		return e
	}
	return backup.Encryption{Enabled: true, Algorithm: backup.EncryptionAlgorithm, KeyDerivation: "scrypt"}
}

func recipientString(r age.Recipient) string {
	if s, ok := r.(fmt.Stringer); ok {
		return s.String()
	}
	return fmt.Sprintf("%T", r)
}

func joinRecipients(rs []age.Recipient) string {
	parts := make([]string, 0, len(rs))
	for _, r := range rs {
		parts = append(parts, recipientString(r))
	}
	sort.Strings(parts)
	return strings.Join(parts, ", ")
}

func copyManifest(m *backup.Manifest) (*backup.Manifest, error) {
	data, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	var out backup.Manifest
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func samePath(a, b string) (bool, error) {
	aa, err := filepath.Abs(a)
	if err != nil {
		return false, err
	}
	bb, err := filepath.Abs(b)
	if err != nil {
		return false, err
	}
	if aa == bb {
		return true, nil
	}
	ai, err1 := os.Stat(aa)
	bi, err2 := os.Stat(bb)
	if err1 != nil || err2 != nil {
		return false, nil
	}
	return os.SameFile(ai, bi), nil
}

// ParseIdentityFile reads age identities (AGE-SECRET-KEY-1… lines;
// `#` comments allowed) from path.
func ParseIdentityFile(path string) ([]age.Identity, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("convert: open identity file: %w", err)
	}
	defer func() { _ = f.Close() }()
	ids, err := age.ParseIdentities(f)
	if err != nil {
		return nil, fmt.Errorf("convert: parse identity file %s: %w", path, err)
	}
	return ids, nil
}

// ParseRecipients parses age1… public keys.
func ParseRecipients(keys []string) ([]age.Recipient, error) {
	var out []age.Recipient
	for _, k := range keys {
		r, err := age.ParseX25519Recipient(strings.TrimSpace(k))
		if err != nil {
			return nil, fmt.Errorf("convert: recipient %q: %w", k, err)
		}
		out = append(out, r)
	}
	return out, nil
}
