package backup

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/crewship-ai/crewship/internal/quota"
)

var snapshotEntryName = regexp.MustCompile(`^[a-f0-9]{64}\.(ext4|json)$`)
var snapshotNamespace = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,127}$`)

func (p *ExtractedPayload) extractServiceSnapshot(ctx context.Context, tr io.Reader, hdr *tar.Header, name string, total *int64) error {
	name = strings.TrimPrefix(name, serviceSnapshotsPrefix)
	if !snapshotEntryName.MatchString(name) || hdr.Typeflag != tar.TypeReg {
		return fmt.Errorf("backup: invalid service snapshot entry")
	}
	if len(p.serviceImages)+len(p.serviceMetadata) >= 8192 {
		return fmt.Errorf("backup: excessive service snapshots")
	}
	id := strings.SplitN(name, ".", 2)[0]
	if strings.HasSuffix(name, ".json") {
		if hdr.Size <= 0 || hdr.Size > 4096 {
			return fmt.Errorf("backup: invalid service snapshot metadata size")
		}
		if _, exists := p.serviceMetadata[id]; exists {
			return fmt.Errorf("backup: duplicate service snapshot metadata")
		}
		raw, err := io.ReadAll(io.LimitReader(tr, 4097))
		if err != nil {
			return err
		}
		var meta serviceSnapshot
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&meta) != nil {
			return fmt.Errorf("backup: invalid service snapshot metadata")
		}
		var trailing any
		if decoder.Decode(&trailing) != io.EOF {
			return fmt.Errorf("backup: trailing service snapshot metadata")
		}
		if meta.name() != id || quota.Validate(meta.key(), meta.Bytes) != nil || !snapshotNamespace.MatchString(meta.Namespace) || !snapshotEntryName.MatchString(meta.SHA256+".json") || (meta.DesiredState != "running" && meta.DesiredState != "stopped") || meta.IntentVersion < 1 {
			return fmt.Errorf("backup: invalid service snapshot identity")
		}
		p.serviceMetadata[id] = meta
		return nil
	}
	if hdr.Size < quota.MinBytes || hdr.Size > quota.MaxBytes || hdr.Size%(1<<20) != 0 || *total > 1<<40-hdr.Size {
		return fmt.Errorf("backup: service image exceeds capacity bounds")
	}
	if _, exists := p.serviceImages[id]; exists {
		return fmt.Errorf("backup: duplicate service image")
	}
	*total += hdr.Size
	file, err := p.storageOrDefault().CreateTemp(ctx, p.tempDir, "quota-image-*.ext4")
	if err != nil {
		return err
	}
	if _, err = io.CopyN(file, tr, hdr.Size); err != nil {
		_ = file.Close()
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	p.serviceImages[id] = file.Name()
	return nil
}

// validateServiceSnapshotArchive runs before every DB write or provider call,
// including dry-run. Counts, exact raw lengths and SHA-256 are all mandatory.
func (p *ExtractedPayload) validateServiceSnapshotArchive(ctx context.Context, count int) error {
	if count < 0 || count != len(p.serviceImages) || count != len(p.serviceMetadata) {
		return fmt.Errorf("backup: service snapshot section count mismatch")
	}
	for id, meta := range p.serviceMetadata {
		path, ok := p.serviceImages[id]
		if !ok {
			return fmt.Errorf("backup: missing service image")
		}
		f, err := p.storageOrDefault().Open(ctx, path)
		if err != nil {
			return err
		}
		h := sha256.New()
		n, copyErr := io.Copy(h, io.LimitReader(f, meta.Bytes+1))
		closeErr := f.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		if n != meta.Bytes || hex.EncodeToString(h.Sum(nil)) != meta.SHA256 {
			return fmt.Errorf("backup: service image checksum mismatch")
		}
	}
	return nil
}
