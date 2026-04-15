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

See .claude/context/prd/DEPLOYMENT.md ("Restore mimo Crewship") in the
Crewship source tree for the full recovery guide.
`

// WriteBundleOptions controls how WriteBundle produces its output. At
// least one of Recipients or Passphrase must be non-empty unless
// NoEncrypt is explicitly true.
type WriteBundleOptions struct {
	Recipients []age.Recipient // asymmetric mode; wins over Passphrase
	Passphrase string          // passphrase mode; ignored when Recipients is set
	NoEncrypt  bool            // explicit opt-out; bundle will carry a plaintext payload
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
			data, err := io.ReadAll(tr)
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
