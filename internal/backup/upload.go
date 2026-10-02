package backup

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"filippo.io/age"
	"github.com/crewship-ai/crewship/internal/diskusage"
	"github.com/klauspost/compress/zstd"
)

var ErrUploadLimit = errors.New("backup upload exceeds the configured size limit")
var ErrUploadSpace = errors.New("not enough free space for backup upload")
var ErrUploadInvalid = errors.New("not a supported encrypted backup bundle")

const DefaultUploadMaxBytes int64 = 64 << 30
const DefaultUploadHeadroomBytes int64 = 256 << 20

type UploadOptions struct {
	Directory     string
	MaxBytes      int64
	MinFreeBytes  int64
	ContentLength int64
	Space         func(string) (uint64, error)
	Finalize      func(context.Context, *sql.Tx, *ImportedBundle) error
}
type ImportedBundle struct {
	Entry              CatalogEntry
	Manifest           *Manifest
	Duplicate          bool
	ConversionRequired bool
}

// ImportUploadedBundle never receives a decryption identity. The initial input
// is encrypted using an ephemeral staging key before touching disk. Only a
// structurally valid AGE bundle with a matching payload digest is published.
func ImportUploadedBundle(ctx context.Context, db *sql.DB, src io.Reader, opts UploadOptions) (_ *ImportedBundle, retErr error) {
	if db == nil || src == nil {
		return nil, ErrUploadInvalid
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if opts.MaxBytes == 0 {
		opts.MaxBytes = DefaultUploadMaxBytes
	}
	if opts.MinFreeBytes == 0 {
		opts.MinFreeBytes = DefaultUploadHeadroomBytes
	}
	if opts.MaxBytes < 1 || opts.MaxBytes > 1<<40 || opts.MinFreeBytes < 0 || opts.MinFreeBytes > 1<<40 {
		return nil, ErrUploadLimit
	}
	if opts.ContentLength > opts.MaxBytes {
		return nil, ErrUploadLimit
	}
	if opts.Directory == "" {
		var err error
		opts.Directory, err = defaultBackupsDirFor(getDefaultStorage())
		if err != nil {
			return nil, err
		}
	}
	directory, err := filepath.Abs(opts.Directory)
	if err != nil {
		return nil, err
	}
	if err = os.MkdirAll(directory, 0700); err != nil {
		return nil, err
	}
	if opts.Space == nil {
		opts.Space = func(path string) (uint64, error) {
			usage, err := diskusage.Usage(path)
			if err != nil {
				return 0, err
			}
			return usage.FreeBytes, nil
		}
	}
	checkSpace := func(need int64) error {
		free, err := opts.Space(directory)
		if err != nil {
			return err
		}
		if need < 0 || free < uint64(need)+uint64(opts.MinFreeBytes) {
			return ErrUploadSpace
		}
		return nil
	}
	if opts.ContentLength > 0 {
		if err = checkSpace(opts.ContentLength * 2); err != nil {
			return nil, err
		}
	} else if err = checkSpace(1); err != nil {
		return nil, err
	}
	staging, err := os.MkdirTemp(directory, uploadStagingPrefix)
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(staging)
	cipher, err := newStagingCipher()
	if err != nil {
		return nil, err
	}
	sealed := filepath.Join(staging, "incoming.sealed")
	incoming, err := cipher.Create(sealed)
	if err != nil {
		return nil, err
	}
	digest := sha256.New()
	count, copyErr := copyUpload(ctx, io.MultiWriter(incoming, digest), io.LimitReader(src, opts.MaxBytes+1), checkSpace)
	closeErr := incoming.Close()
	if err = errors.Join(copyErr, closeErr); err != nil {
		return nil, err
	}
	if count > opts.MaxBytes {
		return nil, ErrUploadLimit
	}
	if opts.ContentLength >= 0 && opts.ContentLength != 0 && count != opts.ContentLength {
		return nil, fmt.Errorf("%w: incomplete request", ErrUploadInvalid)
	}
	reader, _, err := cipher.Open(sealed)
	if err != nil {
		return nil, err
	}
	// The payload is AGE ciphertext and does not compress, so the expanded
	// archive is bounded by what was received plus the small text entries.
	manifest, validateErr := validateUploadedBundle(ctx, reader, count+uploadTextSlackBytes)
	if validateErr == nil {
		_, validateErr = io.Copy(io.Discard, uploadContextReader{ctx, reader})
	}
	closeErr = reader.Close()
	if err = errors.Join(validateErr, closeErr); err != nil {
		return nil, err
	}
	if err = checkSpace(count); err != nil {
		return nil, err
	}
	rawPath := filepath.Join(staging, "validated.tar.zst")
	raw, err := os.OpenFile(rawPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return nil, err
	}
	reader, _, err = cipher.Open(sealed)
	if err != nil {
		raw.Close()
		return nil, err
	}
	_, copyErr = copyUpload(ctx, raw, reader, checkSpace)
	closeErr = reader.Close()
	if copyErr == nil {
		copyErr = raw.Sync()
	}
	rawCloseErr := raw.Close()
	if err = errors.Join(copyErr, closeErr, rawCloseErr); err != nil {
		return nil, err
	}
	finalName := "uploaded-" + hex.EncodeToString(digest.Sum(nil)) + ".tar.zst"
	finalPath := filepath.Join(directory, finalName)
	result := &ImportedBundle{Manifest: manifest, ConversionRequired: manifest.FormatVersion < MinSupportedFormatVersion}
	result.Entry = CatalogEntryFromResult(&CreateResult{Path: finalPath, Size: count, SHA256: manifest.Checksums.PayloadSHA256}, manifest)
	result.Entry.ID, err = newCatalogID()
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	// Hash an existing duplicate before the catalog transaction: its upsert
	// holds the SQLite writer, and every other write would wait out a re-hash
	// of a multi-gigabyte file.
	verified, err := verifyExistingUpload(ctx, root, finalName, count, digest.Sum(nil))
	if err != nil {
		return nil, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	// The upsert acquires the SQLite writer before publication. Other importers
	// using the same catalog cannot observe or delete an uncommitted final file.
	if err = upsertCatalogEntry(ctx, tx, result.Entry); err != nil {
		return nil, err
	}
	result.Entry, err = scanCatalogEntry(tx.QueryRowContext(ctx, `SELECT `+catalogColumns+` FROM backup_catalog WHERE file_path=?`, finalPath))
	if err != nil {
		return nil, err
	}
	fresh := false
	committed := false
	defer func() {
		if fresh && !committed {
			_ = root.Remove(finalName)
		}
	}()
	relative, err := filepath.Rel(directory, rawPath)
	if err != nil {
		return nil, err
	}
	if err = root.Link(relative, finalName); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		info, statErr := root.Lstat(finalName)
		if statErr != nil || !info.Mode().IsRegular() || info.Size() != count {
			return nil, ErrUploadInvalid
		}
		// Only a file published after the check above, by a concurrent
		// upload of the same bytes, is hashed under the transaction.
		if verified == nil || !os.SameFile(verified, info) || !verified.ModTime().Equal(info.ModTime()) {
			if verified, err = verifyExistingUpload(ctx, root, finalName, count, digest.Sum(nil)); err != nil {
				return nil, err
			}
			if verified == nil {
				return nil, ErrUploadInvalid
			}
		}
		result.Duplicate = true
	} else {
		fresh = true
	}
	parent, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	err = parent.Sync()
	closeErr = parent.Close()
	if err = errors.Join(err, closeErr); err != nil {
		return nil, err
	}
	if opts.Finalize != nil {
		if err = opts.Finalize(ctx, tx, result); err != nil {
			return nil, err
		}
	}
	if err = tx.Commit(); err != nil {
		_ = tx.Rollback()
		// An uncertain commit must never unlink data a committed row references.
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		entry, queryErr := GetCatalogEntry(cleanupCtx, db, finalPath)
		if queryErr != nil && !errors.Is(queryErr, ErrCatalogEntryNotFound) || queryErr == nil && entry.ID == result.Entry.ID {
			committed = true
		}
		return nil, err
	}
	committed = true
	return result, nil
}

// uploadStagingPrefix names an upload's staging directory. A crash leaves it
// behind; SweepInstanceStaging removes it on the next start.
const uploadStagingPrefix = ".upload-"

// uploadTextSlackBytes bounds the manifest and restore notes that may expand
// beyond the received bytes when the archive is decompressed.
const uploadTextSlackBytes = maxBackupManifestBytes + 64<<10

// verifyExistingUpload returns the file already published under name when it
// holds exactly the uploaded bytes, nil when no file exists, and
// ErrUploadInvalid for anything else under that name.
func verifyExistingUpload(ctx context.Context, root *os.Root, name string, size int64, sum []byte) (os.FileInfo, error) {
	info, err := root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Size() != size {
		return nil, ErrUploadInvalid
	}
	existing, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	opened, err := existing.Stat()
	if err != nil || !os.SameFile(info, opened) || opened.Size() != size {
		existing.Close()
		return nil, ErrUploadInvalid
	}
	hash := sha256.New()
	_, hashErr := io.Copy(hash, uploadContextReader{ctx, existing})
	closeErr := existing.Close()
	if err = errors.Join(hashErr, closeErr); err != nil {
		return nil, err
	}
	if !bytes.Equal(hash.Sum(nil), sum) {
		return nil, ErrUploadInvalid
	}
	return opened, nil
}

type uploadContextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r uploadContextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}
func copyUpload(ctx context.Context, dst io.Writer, src io.Reader, space func(int64) error) (int64, error) {
	buffer := make([]byte, 64<<10)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		if err := space(int64(len(buffer))); err != nil {
			return total, err
		}
		n, readErr := src.Read(buffer)
		if n > 0 {
			written, err := dst.Write(buffer[:n])
			total += int64(written)
			if err != nil {
				return total, err
			}
			if written != n {
				return total, io.ErrShortWrite
			}
		}
		if readErr == io.EOF {
			return total, nil
		}
		if readErr != nil {
			return total, readErr
		}
	}
}

// The outer archive is validated without unpacking any payload. This also
// rejects duplicate entries, tar links, trailing hidden data and expansion
// beyond the same bound used for the incoming stream.
func validateUploadedBundle(ctx context.Context, src io.Reader, maxBytes int64) (*Manifest, error) {
	decoder, err := zstd.NewReader(uploadContextReader{ctx, src}, zstd.WithDecoderMaxMemory(64<<20), zstd.WithDecoderConcurrency(1))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUploadInvalid, err)
	}
	defer decoder.Close()
	archive := tar.NewReader(decoder)
	var manifest *Manifest
	seen := map[string]bool{}
	var expanded int64
	for {
		header, err := archive.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrUploadInvalid, err)
		}
		if header.Typeflag != tar.TypeReg || seen[header.Name] || header.Size < 0 || header.Size > maxBytes+maxBackupManifestBytes-expanded {
			return nil, ErrUploadInvalid
		}
		seen[header.Name] = true
		expanded += header.Size
		switch header.Name {
		case manifestFileName:
			if header.Size > maxBackupManifestBytes {
				return nil, ErrUploadInvalid
			}
			data, err := io.ReadAll(archive)
			if err != nil {
				return nil, ErrUploadInvalid
			}
			manifest, err = ReadManifest(data)
			if err != nil {
				return nil, fmt.Errorf("%w: %v", ErrUploadInvalid, err)
			}
			if manifest.FormatVersion < OldestRecoverableFormatVersion || manifest.FormatVersion > FormatVersion || !manifest.Encryption.Enabled || manifest.Encryption.Algorithm != EncryptionAlgorithm {
				return nil, ErrUploadInvalid
			}
			if manifest.Scope == ScopeInstance && manifest.Contents.Instance == nil || manifest.Scope == ScopeWorkspace && manifest.Contents.Workspace == nil || manifest.Scope == ScopeCrew && len(manifest.Contents.Crews) == 0 {
				return nil, ErrUploadInvalid
			}
		case restoreReadmeName:
			if header.Size > 64<<10 {
				return nil, ErrUploadInvalid
			}
			if _, err = io.Copy(io.Discard, archive); err != nil {
				return nil, ErrUploadInvalid
			}
		case payloadFileName:
			if manifest == nil || header.Size > maxBytes {
				return nil, ErrUploadInvalid
			}
			hashed := NewHashingReader(archive)
			// With no identities AGE validates only its envelope. No plaintext is
			// obtained and no contents/restorability claim is made here.
			_, ageErr := age.Decrypt(io.LimitReader(hashed, 256<<10), uploadEnvelopeIdentity{})
			var missingIdentity *age.NoIdentityMatchError
			if !errors.As(ageErr, &missingIdentity) {
				return nil, ErrUploadInvalid
			}
			if _, err = io.Copy(io.Discard, hashed); err != nil {
				return nil, ErrUploadInvalid
			}
			if err = VerifyChecksum(manifest.Checksums.PayloadSHA256, hashed.Sum()); err != nil {
				return nil, fmt.Errorf("%w: payload checksum mismatch", ErrUploadInvalid)
			}
		default:
			return nil, ErrUploadInvalid
		}
	}
	if manifest == nil || !seen[payloadFileName] {
		return nil, ErrUploadInvalid
	}
	padding, err := io.ReadAll(io.LimitReader(decoder, 16385))
	if err != nil || len(padding) > 16384 || len(bytes.Trim(padding, "\x00")) != 0 {
		return nil, ErrUploadInvalid
	}
	return manifest, nil
}

// This identity deliberately matches nothing. It lets AGE parse its envelope
// without possessing, generating or persisting any decryption key.
type uploadEnvelopeIdentity struct{}

func (uploadEnvelopeIdentity) Unwrap([]*age.Stanza) ([]byte, error) {
	return nil, age.ErrIncorrectIdentity
}
