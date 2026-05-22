package backup

import (
	"bytes"
	"fmt"
	"io"
	"time"

	"filippo.io/age"
)

// Bundle layout written by WriteBundle and consumed by ReadBundle:
//
//   outer tar.zst
//     ├── MANIFEST.json         always plaintext
//     ├── RESTORE.md            short human pointer (plaintext)
//     └── payload               raw bytes; AGE-encrypted if manifest says so,
//                               otherwise a nested tar.zst of payload files
//
// The "payload" member is a single tar entry to keep the reader simple:
// callers stream into a payload-sized io.Reader and own any nested
// archive semantics themselves.

const (
	manifestFileName  = "MANIFEST.json"
	restoreReadmeName = "RESTORE.md"
	payloadFileName   = "payload"

	// Per-entry caps for io.ReadAll on attacker-controllable tar
	// content during restore / inspect. The tar header carries the
	// claimed size; io.ReadAll allocates that much memory before
	// reading the entry. Without these wrappers a malicious bundle
	// can hdr.Size = 10 GB and crash the restorer.
	//
	// Devcontainer feature blobs (devcontainer.json + mise.toml) are
	// kilobyte-scale; 5 MB leaves plenty of headroom and denies the
	// allocation bomb.
	maxBackupDevcontainerEntryBytes int64 = 5 << 20 // 5 MB

	// Manifest is JSON describing the bundle; ours run < 64 KB in
	// practice. 4 MB is the safety margin for forwards-compatible
	// schema additions while still bounded.
	maxBackupManifestBytes int64 = 4 << 20 // 4 MB

	// DB dump JSON is the largest legitimate entry — a sqlite dump
	// of a busy workspace. 500 MB is generous for the dump shape we
	// emit today; if a real workspace exceeds it, the bound is the
	// signal to switch dump format, not to lift the cap.
	maxBackupDBDumpBytes int64 = 500 << 20 // 500 MB

	// Canary file written by selftest's CopyFromContainer round-trip.
	// 1 KB is more than the canary ever needs; if a malicious tar
	// claims 10 GB for the canary, we want to drop it fast.
	maxBackupCanaryBytes int64 = 1 << 10 // 1 KB
)

// RestoreReadmeContent is the canonical copy that every bundle ships
// with. Bundled as a string constant so all backups speak the same
// instructions regardless of the binary that wrote them.
const RestoreReadmeContent = `# Crewship backup bundle

This archive was produced by ` + "`crewship backup create`" + `.

To restore:

1. Preferred: use the Crewship CLI from the same or a newer version.

       crewship backup restore <this-file>

2. Data-only extraction (no Crewship installed):

       tar --zstd -xf <this-file>
       # If the manifest reports encryption.enabled: true, the payload
       # file is AGE-encrypted (https://age-encryption.org/).
       age -d -i <your-key-file> payload > payload.tar
       # Or for passphrase-encrypted bundles:
       age -d -p payload > payload.tar
       tar -xf payload.tar
`

// WriteBundleOptions controls how WriteBundle produces its output. At
// least one of Recipients or Passphrase must be non-empty unless
// NoEncrypt is explicitly true.
type WriteBundleOptions struct {
	Recipients []age.Recipient // asymmetric mode; wins over Passphrase
	Passphrase string          // passphrase mode; ignored when Recipients is set
	NoEncrypt  bool            // explicit opt-out; bundle will carry a plaintext payload
}

// SealPayload streams raw payload through the configured encryption
// path into dst and returns (sha256, bytes-written). It does NOT write
// the outer bundle — callers assemble that with WriteBundleStream.
//
// This exists so multi-GB bundles can flow through disk-backed temp
// files instead of being held twice in memory (once for payload, once
// for sealed output) as WriteBundle does.
func SealPayload(dst io.Writer, src io.Reader, opts WriteBundleOptions) (string, int64, error) {
	// dst → counting → hashing. Sealed bytes flow: AGE writer → hasher
	// → counter → dst. The counter gives us the tar header size without
	// a second pass over the file.
	counter := &countingWriter{w: dst}
	hasher := NewHashingWriter(counter)
	switch {
	case opts.NoEncrypt:
		if _, err := io.Copy(hasher, src); err != nil {
			return "", 0, fmt.Errorf("backup: copy plaintext payload: %w", err)
		}
		return hasher.Sum(), counter.n, nil
	case len(opts.Recipients) > 0:
		w, err := EncryptStream(hasher, opts.Recipients...)
		if err != nil {
			return "", 0, err
		}
		if _, err := io.Copy(w, src); err != nil {
			_ = w.Close()
			return "", 0, fmt.Errorf("backup: copy encrypted payload: %w", err)
		}
		if err := w.Close(); err != nil {
			return "", 0, fmt.Errorf("backup: close AGE writer: %w", err)
		}
		return hasher.Sum(), counter.n, nil
	case opts.Passphrase != "":
		w, err := EncryptStreamPassphrase(hasher, opts.Passphrase)
		if err != nil {
			return "", 0, err
		}
		if _, err := io.Copy(w, src); err != nil {
			_ = w.Close()
			return "", 0, fmt.Errorf("backup: copy encrypted payload: %w", err)
		}
		if err := w.Close(); err != nil {
			return "", 0, fmt.Errorf("backup: close AGE writer: %w", err)
		}
		return hasher.Sum(), counter.n, nil
	default:
		return "", 0, fmt.Errorf("backup: SealPayload requires Recipients, Passphrase, or NoEncrypt=true")
	}
}

// WriteBundleStream assembles a finalised bundle given already-sealed
// payload bytes of known size. The caller must populate manifest.Scope,
// manifest.Encryption and manifest.Checksums.PayloadSHA256 before
// calling; this function only writes outer tar headers and bodies.
//
// Used by the runner to stream GB-scale payloads through disk without
// doubling up in memory. WriteBundle remains available for small-scale
// tests that don't care about peak memory.
func WriteBundleStream(sink io.Writer, manifest *Manifest, sealed io.Reader, sealedSize int64) error {
	if manifest == nil {
		return fmt.Errorf("backup: WriteBundleStream: manifest is nil")
	}
	if manifest.FormatVersion == 0 {
		manifest.FormatVersion = FormatVersion
	}
	if err := manifest.Validate(); err != nil {
		return fmt.Errorf("backup: manifest validate: %w", err)
	}

	var manifestBuf bytes.Buffer
	if _, err := manifest.WriteTo(&manifestBuf); err != nil {
		return fmt.Errorf("backup: marshal manifest: %w", err)
	}

	tw, err := NewTarZstWriter(sink)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	if err := tw.WriteFile(manifestFileName, 0o644, now, manifestBuf.Bytes()); err != nil {
		return err
	}
	if err := tw.WriteFile(restoreReadmeName, 0o644, now, []byte(RestoreReadmeContent)); err != nil {
		return err
	}
	if err := tw.WriteStream(payloadFileName, 0o600, now, sealedSize, sealed); err != nil {
		return err
	}
	return tw.Close()
}

// ReadBundleStream is the streaming counterpart to ReadBundle. It
// returns the parsed manifest and a Reader that yields the sealed
// payload bytes as they come off the outer tar, plus a Close function
// the caller MUST invoke once consumption is complete. Checksum
// verification is the caller's responsibility — wrap the returned
// reader with NewHashingReader and call VerifyChecksum when done.
//
// Unlike ReadBundle this never buffers the full payload in memory;
// consuming a multi-GB bundle stays within a few MB of heap.
func ReadBundleStream(src io.Reader) (*Manifest, io.Reader, func() error, error) {
	tr, err := NewTarZstReader(src)
	if err != nil {
		return nil, nil, nil, err
	}
	closer := func() error { return tr.Close() }

	var manifest *Manifest
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			_ = tr.Close()
			return nil, nil, nil, fmt.Errorf("backup: read bundle entry: %w", err)
		}
		switch hdr.Name {
		case manifestFileName:
			// Bound the read: a malicious bundle can claim hdr.Size = 10 GB
			// and io.ReadAll would allocate it before we ever validate the
			// manifest shape. PR #493 follow-up: backup-side tar-bomb cap.
			data, err := io.ReadAll(io.LimitReader(tr, maxBackupManifestBytes))
			if err != nil {
				_ = tr.Close()
				return nil, nil, nil, fmt.Errorf("backup: read manifest: %w", err)
			}
			m, err := ReadManifest(data)
			if err != nil {
				_ = tr.Close()
				return nil, nil, nil, err
			}
			manifest = m
		case restoreReadmeName:
			if _, err := io.Copy(io.Discard, tr); err != nil {
				_ = tr.Close()
				return nil, nil, nil, fmt.Errorf("backup: skip RESTORE.md: %w", err)
			}
		case payloadFileName:
			if manifest == nil {
				_ = tr.Close()
				return nil, nil, nil, fmt.Errorf("%w: payload entry appeared before MANIFEST.json", ErrInvalidManifest)
			}
			if err := CompatibilityReason(manifest.FormatVersion); err != nil {
				_ = tr.Close()
				return manifest, nil, nil, err
			}
			// The tar reader itself is the streaming payload source.
			// Caller consumes and then calls closer() (which closes the
			// outer zstd decoder). We do not advance past this entry —
			// the payload is the last one the writer emitted.
			return manifest, tr, closer, nil
		default:
			if _, err := io.Copy(io.Discard, tr); err != nil {
				_ = tr.Close()
				return nil, nil, nil, fmt.Errorf("backup: skip %q: %w", hdr.Name, err)
			}
		}
	}
	_ = tr.Close()
	if manifest == nil {
		return nil, nil, nil, fmt.Errorf("%w: MANIFEST.json missing from bundle", ErrInvalidManifest)
	}
	return manifest, nil, nil, fmt.Errorf("%w: payload missing from bundle", ErrInvalidManifest)
}

// WriteBundle assembles a complete bundle at sink. The payload bytes
// supplied in payload are sealed according to opts and placed in the
// outer tar alongside the manifest and RESTORE.md. The manifest's
// Checksums.PayloadSHA256 and Encryption fields are populated by this
// function and MUST NOT be set by the caller.
func WriteBundle(sink io.Writer, manifest *Manifest, payload io.Reader, opts WriteBundleOptions) error {
	if manifest == nil {
		return fmt.Errorf("backup: WriteBundle: manifest is nil")
	}
	if manifest.FormatVersion == 0 {
		manifest.FormatVersion = FormatVersion
	}

	// Seal the payload into a buffer so we can compute its size and
	// checksum before writing tar headers. Streaming-all-the-way-down
	// is a V2 optimisation that needs a two-pass tar format — out of
	// scope for MVP. Expected payloads are on the order of GB which
	// fits comfortably into tmpfs on any reasonable host.
	var sealed bytes.Buffer
	hasher := NewHashingWriter(&sealed)

	switch {
	case opts.NoEncrypt:
		manifest.Encryption = Encryption{Enabled: false}
		if _, err := io.Copy(hasher, payload); err != nil {
			return fmt.Errorf("backup: copy plaintext payload: %w", err)
		}
	case len(opts.Recipients) > 0:
		manifest.Encryption = Encryption{
			Enabled:   true,
			Algorithm: EncryptionAlgorithm,
		}
		for _, r := range opts.Recipients {
			manifest.Encryption.Recipients = append(manifest.Encryption.Recipients, recipientString(r))
		}
		w, err := EncryptStream(hasher, opts.Recipients...)
		if err != nil {
			return err
		}
		if _, err := io.Copy(w, payload); err != nil {
			_ = w.Close()
			return fmt.Errorf("backup: copy encrypted payload: %w", err)
		}
		if err := w.Close(); err != nil {
			return fmt.Errorf("backup: close AGE writer: %w", err)
		}
	case opts.Passphrase != "":
		manifest.Encryption = Encryption{
			Enabled:       true,
			Algorithm:     EncryptionAlgorithm,
			KeyDerivation: "scrypt",
		}
		w, err := EncryptStreamPassphrase(hasher, opts.Passphrase)
		if err != nil {
			return err
		}
		if _, err := io.Copy(w, payload); err != nil {
			_ = w.Close()
			return fmt.Errorf("backup: copy encrypted payload: %w", err)
		}
		if err := w.Close(); err != nil {
			return fmt.Errorf("backup: close AGE writer: %w", err)
		}
	default:
		return fmt.Errorf("backup: WriteBundle requires Recipients, Passphrase, or NoEncrypt=true")
	}
	manifest.Checksums.PayloadSHA256 = hasher.Sum()

	if err := manifest.Validate(); err != nil {
		return fmt.Errorf("backup: manifest validate: %w", err)
	}

	// Marshal manifest once, after all derived fields are populated.
	var manifestBuf bytes.Buffer
	if _, err := manifest.WriteTo(&manifestBuf); err != nil {
		return fmt.Errorf("backup: marshal manifest: %w", err)
	}

	// Outer tar.zst: MANIFEST.json, RESTORE.md, payload.
	tw, err := NewTarZstWriter(sink)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	if err := tw.WriteFile(manifestFileName, 0o644, now, manifestBuf.Bytes()); err != nil {
		return err
	}
	if err := tw.WriteFile(restoreReadmeName, 0o644, now, []byte(RestoreReadmeContent)); err != nil {
		return err
	}
	if err := tw.WriteStream(payloadFileName, 0o600, now, int64(sealed.Len()), &sealed); err != nil {
		return err
	}
	return tw.Close()
}

// ReadBundle pulls the manifest and payload out of the outer tar.
// The returned payload reader yields the sealed bytes exactly as they
// were written — callers that encounter `manifest.Encryption.Enabled`
// are responsible for running the bytes through DecryptStream* before
// extracting the nested archive. This split keeps key material out of
// ReadBundle's signature.
//
// Checksum verification (HashingReader + VerifyChecksum) happens on
// the caller side so that the same reader can tee bytes into the
// decryption pipeline without re-reading the bundle.
func ReadBundle(src io.Reader) (*Manifest, io.Reader, error) {
	tr, err := NewTarZstReader(src)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = tr.Close() }()

	var manifest *Manifest
	var payloadBuf bytes.Buffer
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, nil, fmt.Errorf("backup: read bundle entry: %w", err)
		}
		switch hdr.Name {
		case manifestFileName:
			// PR #493 follow-up: bound manifest read so a 10 GB hdr.Size
			// can't OOM the inspector before we validate shape.
			data, err := io.ReadAll(io.LimitReader(tr, maxBackupManifestBytes))
			if err != nil {
				return nil, nil, fmt.Errorf("backup: read manifest: %w", err)
			}
			m, err := ReadManifest(data)
			if err != nil {
				return nil, nil, err
			}
			manifest = m
		case restoreReadmeName:
			// Informational; discard.
			if _, err := io.Copy(io.Discard, tr); err != nil {
				return nil, nil, fmt.Errorf("backup: skip RESTORE.md: %w", err)
			}
		case payloadFileName:
			if _, err := io.Copy(&payloadBuf, tr); err != nil {
				return nil, nil, fmt.Errorf("backup: read payload: %w", err)
			}
		default:
			// Forward-compat: future writers may add entries. Ignore
			// unknown names rather than erroring out.
			if _, err := io.Copy(io.Discard, tr); err != nil {
				return nil, nil, fmt.Errorf("backup: skip %q: %w", hdr.Name, err)
			}
		}
	}

	if manifest == nil {
		return nil, nil, fmt.Errorf("%w: MANIFEST.json missing from bundle", ErrInvalidManifest)
	}
	if err := CompatibilityReason(manifest.FormatVersion); err != nil {
		return manifest, nil, err
	}
	return manifest, &payloadBuf, nil
}

// recipientString returns a stable string identifier for a recipient
// so it can be recorded in the manifest. AGE's X25519Recipient
// implements fmt.Stringer; ScryptRecipient does not leak anything
// sensitive, so fmt.Sprintf is a safe fallback.
func recipientString(r age.Recipient) string {
	if s, ok := r.(fmt.Stringer); ok {
		return s.String()
	}
	return fmt.Sprintf("%T", r)
}
