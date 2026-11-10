package artifacts

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// A Library is an artifact project whose design-system.json other projects
// consume (#5189). Publishing a version is separate from editing it: the
// caller's authorize function must admit only the library's owner. A consumer
// keeps a self-contained copy of the published file, so rendering it never
// reads the library again; restoring, editing or deleting the library, or
// losing access to it, never changes a consumer.
const (
	LibraryFile     = "design-system.json"
	MaxLibraryBytes = 32 << 10
	// maxLibraryChanges bounds each list in an update review.
	maxLibraryChanges = 200
)

// ErrLibraryUnreadable means the caller cannot read the library version a pin
// would copy. It never says whether the library or the version exists.
var ErrLibraryUnreadable = errors.New("library is not readable by this caller")

// ErrLibraryPinCorrupt means the stored copy no longer matches its SHA-256.
var ErrLibraryPinCorrupt = errors.New("pinned library copy does not match its checksum")

// LibraryVersionAuthorize checks, inside the caller's transaction, live read
// authority over one library and that the caller may read the given version.
// It must return the same actor as the consumer's authorize function, and
// should return ErrLibraryUnreadable for every denial.
type LibraryVersionAuthorize func(ctx context.Context, tx *sql.Tx, version int64) (string, error)

type LibraryPublication struct {
	Version     int64  `json:"published_version"`
	PublishedBy string `json:"published_by,omitempty"`
	PublishedAt string `json:"published_at,omitempty"`
}

type LibraryPin struct {
	LibraryPageID  string `json:"library_page_id"`
	LibraryVersion int64  `json:"library_version"`
	LibraryName    string `json:"library_name"`
	Content        []byte `json:"-"`
	SHA256         string `json:"sha256"`
	CopiedBy       string `json:"copied_by"`
	CopiedAt       string `json:"copied_at"`
}

type LibraryChanges struct {
	Added   []string `json:"added"`
	Removed []string `json:"removed"`
	Changed []string `json:"changed"`
	// Truncated is true when any list was cut at maxLibraryChanges.
	Truncated bool `json:"truncated,omitempty"`
}

type LibraryUpdate struct {
	LibraryPageID string `json:"library_page_id"`
	PinnedVersion int64  `json:"pinned_version"`
	// PublishedVersion is 0 when the library is not readable or not published.
	PublishedVersion int64          `json:"published_version"`
	LibraryAvailable bool           `json:"library_available"`
	UpdateAvailable  bool           `json:"update_available"`
	Changes          LibraryChanges `json:"changes"`
}

func libraryTokens(m VersionManifest) ([]byte, error) {
	f, ok := m.Files[LibraryFile]
	if !ok {
		return nil, fmt.Errorf("%w: library version has no %s", ErrVersionInvalid, LibraryFile)
	}
	if len(f.Content) > MaxLibraryBytes {
		return nil, fmt.Errorf("%w: %s exceeds %d bytes", ErrVersionInvalid, LibraryFile, MaxLibraryBytes)
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(f.Content, &obj); err != nil || obj == nil {
		return nil, fmt.Errorf("%w: %s must be a JSON object", ErrVersionInvalid, LibraryFile)
	}
	return f.Content, nil
}

func readLibraryPublication(ctx context.Context, tx *sql.Tx, k VersionKey) (p LibraryPublication, err error) {
	err = tx.QueryRowContext(ctx, `SELECT version,published_by,published_at FROM artifact_library_publications WHERE workspace_id=? AND page_id=?`, k.WorkspaceID, k.PageID).Scan(&p.Version, &p.PublishedBy, &p.PublishedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return LibraryPublication{}, nil
	}
	return
}

// PublishLibrary points the library's publication at an existing version that
// carries a valid design-system.json. Re-publishing the current version is a
// no-op. Consumers are never changed by this call.
func (s *VersionStore) PublishLibrary(ctx context.Context, k VersionKey, version int64, a VersionAuthorize) (out LibraryPublication, err error) {
	if version < 1 {
		return LibraryPublication{}, fmt.Errorf("%w: version", ErrVersionInvalid)
	}
	err = s.transaction(ctx, k, a, func(tx *sql.Tx, actor string) error {
		m, e := readVersion(ctx, tx, k, version)
		if e != nil {
			return e
		}
		if _, e = libraryTokens(m); e != nil {
			return e
		}
		if out, e = readLibraryPublication(ctx, tx, k); e != nil || out.Version == version {
			return e
		}
		out = LibraryPublication{version, actor, time.Now().UTC().Format("2006-01-02T15:04:05.000Z")}
		_, e = tx.ExecContext(ctx, `INSERT INTO artifact_library_publications(workspace_id,page_id,version,published_by,published_at) VALUES(?,?,?,?,?) ON CONFLICT(workspace_id,page_id) DO UPDATE SET version=excluded.version,published_by=excluded.published_by,published_at=excluded.published_at`, k.WorkspaceID, k.PageID, version, actor, out.PublishedAt)
		return e
	})
	if err != nil {
		return LibraryPublication{}, err
	}
	return
}

// ReadLibraryPublication returns the library's publication; Version is 0 when
// nothing is published yet.
func (s *VersionStore) ReadLibraryPublication(ctx context.Context, k VersionKey, a VersionAuthorize) (out LibraryPublication, err error) {
	err = s.transaction(ctx, k, a, func(tx *sql.Tx, _ string) error { var e error; out, e = readLibraryPublication(ctx, tx, k); return e })
	if err != nil {
		return LibraryPublication{}, err
	}
	return
}

func readLibraryPin(ctx context.Context, tx *sql.Tx, k VersionKey) (p LibraryPin, found bool, err error) {
	err = tx.QueryRowContext(ctx, `SELECT library_page_id,library_version,library_name,content,sha256,copied_by,copied_at FROM artifact_library_pins WHERE workspace_id=? AND page_id=?`, k.WorkspaceID, k.PageID).Scan(&p.LibraryPageID, &p.LibraryVersion, &p.LibraryName, &p.Content, &p.SHA256, &p.CopiedBy, &p.CopiedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return LibraryPin{}, false, nil
	}
	if err != nil {
		return LibraryPin{}, false, err
	}
	if sum := sha256.Sum256(p.Content); hex.EncodeToString(sum[:]) != p.SHA256 {
		p.Content = nil // A repair may compare metadata; never hand out damaged bytes.
		return p, true, ErrLibraryPinCorrupt
	}
	return p, true, nil
}

// ReadLibraryPin returns the consumer's pinned copy under the consumer's own
// authority only; the library is not consulted.
func (s *VersionStore) ReadLibraryPin(ctx context.Context, consumer VersionKey, a VersionAuthorize) (out LibraryPin, err error) {
	err = s.transaction(ctx, consumer, a, func(tx *sql.Tx, _ string) error {
		var found bool
		var e error
		if out, found, e = readLibraryPin(ctx, tx, consumer); e == nil && !found {
			e = ErrVersionNotFound
		}
		return e
	})
	if err != nil {
		return LibraryPin{}, err
	}
	return
}

// libraryRead runs the library authority for one version and requires it to
// name the consumer's actor.
func libraryRead(ctx context.Context, tx *sql.Tx, la LibraryVersionAuthorize, version int64, actor string) error {
	if la == nil {
		return ErrLibraryUnreadable
	}
	got, err := la(ctx, tx, version)
	if err != nil {
		return err
	}
	if got != actor {
		return ErrVersionAuthority
	}
	return nil
}

// LibraryPinExpect is the state the caller reviewed. Applying a pin only
// succeeds when the library still publishes Version and the consumer still
// pins PinnedLibrary at PinnedVersion (both empty/0 when nothing is pinned).
type LibraryPinExpect struct {
	Version       int64
	PinnedLibrary string
	PinnedVersion int64
}

// PinLibrary copies the library's published design-system.json into the
// consumer. Any difference from the reviewed state (another published version,
// another pinned library or version) is ErrVersionConflict, so an update is
// always an explicit decision on exactly what was reviewed. Repeating a pin
// that already holds the reviewed version is a no-op (changed=false), which
// makes retries safe.
func (s *VersionStore) PinLibrary(ctx context.Context, consumer VersionKey, libraryPageID string, expect LibraryPinExpect, a VersionAuthorize, la LibraryVersionAuthorize) (LibraryPin, bool, error) {
	return s.pinLibrary(ctx, consumer, libraryPageID, expect, a, func(string) LibraryVersionAuthorize { return la }, nil, false)
}

// PinLibraryByReference resolves client-provided slug or @id references inside
// the writer fence, after consumer write authority. The expected prior pin's
// reference is compared only after admission to the new source publication.
// record runs only for a changed pin, in the same transaction as the copy.
func (s *VersionStore) PinLibraryByReference(ctx context.Context, consumer VersionKey, library string, expect LibraryPinExpect, a VersionAuthorize, authorizeLibrary func(string) LibraryVersionAuthorize, record func(context.Context, *sql.Tx, string, string) error) (LibraryPin, bool, error) {
	return s.pinLibrary(ctx, consumer, library, expect, a, authorizeLibrary, record, true)
}

func resolveLibraryReference(ctx context.Context, tx *sql.Tx, workspace, ref string) (string, error) {
	column := "slug"
	if strings.HasPrefix(ref, "@") {
		column, ref = "id", strings.TrimPrefix(ref, "@")
	}
	var id string
	err := tx.QueryRowContext(ctx, `SELECT id FROM pages WHERE workspace_id=? AND `+column+`=?`, workspace, ref).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrLibraryUnreadable
	}
	return id, err
}

func (s *VersionStore) pinLibrary(ctx context.Context, consumer VersionKey, library string, expect LibraryPinExpect, a VersionAuthorize, authorizeLibrary func(string) LibraryVersionAuthorize, record func(context.Context, *sql.Tx, string, string) error, byReference bool) (out LibraryPin, changed bool, err error) {
	if library == "" {
		return LibraryPin{}, false, fmt.Errorf("%w: library required", ErrVersionInvalid)
	}
	if expect.Version < 1 || expect.PinnedVersion < 0 || (expect.PinnedVersion == 0) != (expect.PinnedLibrary == "") {
		return LibraryPin{}, false, fmt.Errorf("%w: reviewed version, and pinned library with pinned version, required", ErrVersionInvalid)
	}
	err = s.writeTransaction(ctx, consumer, a, func(tx *sql.Tx, actor string) error {
		libraryPageID := library
		if byReference {
			var e error
			if libraryPageID, e = resolveLibraryReference(ctx, tx, consumer.WorkspaceID, library); e != nil {
				return e
			}
		}
		if libraryPageID == consumer.PageID {
			return fmt.Errorf("%w: library must be another project in this workspace", ErrVersionInvalid)
		}
		lib := VersionKey{consumer.WorkspaceID, libraryPageID}
		la := authorizeLibrary(libraryPageID)
		pub, e := readLibraryPublication(ctx, tx, lib)
		if e != nil {
			return e
		}
		if pub.Version == 0 {
			// Indistinguishable from an unreadable or absent library on purpose.
			return ErrLibraryUnreadable
		}
		if e = libraryRead(ctx, tx, la, pub.Version, actor); e != nil {
			return e
		}
		m, e := readVersion(ctx, tx, lib, pub.Version)
		if e != nil {
			return e
		}
		content, e := libraryTokens(m)
		if e != nil {
			return e
		}
		sum := sha256.Sum256(content)
		hash := hex.EncodeToString(sum[:])
		current, found, e := readLibraryPin(ctx, tx, consumer)
		corrupt := errors.Is(e, ErrLibraryPinCorrupt)
		if e != nil && !corrupt {
			return e
		}
		if pub.Version != expect.Version {
			return ErrVersionConflict
		}
		expectedID := expect.PinnedLibrary
		if byReference && expectedID != "" {
			if strings.HasPrefix(expectedID, "@") {
				// The copy may outlive its library; compare the supplied immutable ID.
				expectedID = strings.TrimPrefix(expectedID, "@")
			} else {
				if expectedID, e = resolveLibraryReference(ctx, tx, consumer.WorkspaceID, expectedID); errors.Is(e, ErrLibraryUnreadable) {
					return ErrVersionConflict
				} else if e != nil {
					return e
				}
			}
		}
		if !corrupt && found && current.LibraryPageID == libraryPageID && current.LibraryVersion == pub.Version && current.SHA256 == hash {
			out = current
			return libraryRead(ctx, tx, la, pub.Version, actor)
		}
		if current.LibraryPageID != expectedID || current.LibraryVersion != expect.PinnedVersion {
			return ErrVersionConflict
		}
		var name string
		if e = tx.QueryRowContext(ctx, `SELECT coalesce(name,'') FROM pages WHERE id=? AND workspace_id=?`, libraryPageID, consumer.WorkspaceID).Scan(&name); e != nil {
			return e
		}
		out = LibraryPin{libraryPageID, pub.Version, name, content, hash, actor, time.Now().UTC().Format("2006-01-02T15:04:05.000Z")}
		if _, e = tx.ExecContext(ctx, `INSERT INTO artifact_library_pins(workspace_id,page_id,library_page_id,library_version,library_name,content,sha256,copied_by,copied_at) VALUES(?,?,?,?,?,?,?,?,?) ON CONFLICT(workspace_id,page_id) DO UPDATE SET library_page_id=excluded.library_page_id,library_version=excluded.library_version,library_name=excluded.library_name,content=excluded.content,sha256=excluded.sha256,copied_by=excluded.copied_by,copied_at=excluded.copied_at`, consumer.WorkspaceID, consumer.PageID, out.LibraryPageID, out.LibraryVersion, out.LibraryName, out.Content, out.SHA256, out.CopiedBy, out.CopiedAt); e != nil {
			return e
		}
		if record != nil {
			if e = record(ctx, tx, actor, libraryPageID); e != nil {
				return e
			}
		}
		changed = true
		return libraryRead(ctx, tx, la, pub.Version, actor)
	})
	if err != nil {
		return LibraryPin{}, false, err
	}
	return
}

// CheckLibraryUpdate compares the consumer's pin with the library's current
// publication. An unreadable library is reported as unavailable with no
// version or changes, never as an error, because the consumer still renders.
// An update is available whenever the published version differs from the
// pinned one: an owner who republishes an older version is offering it too.
func (s *VersionStore) CheckLibraryUpdate(ctx context.Context, consumer VersionKey, a VersionAuthorize, library func(libraryPageID string) LibraryVersionAuthorize) (out LibraryUpdate, err error) {
	err = s.transaction(ctx, consumer, a, func(tx *sql.Tx, actor string) error {
		pin, found, e := readLibraryPin(ctx, tx, consumer)
		if e != nil {
			return e
		}
		if !found {
			return ErrVersionNotFound
		}
		out = LibraryUpdate{LibraryPageID: pin.LibraryPageID, PinnedVersion: pin.LibraryVersion, Changes: LibraryChanges{Added: []string{}, Removed: []string{}, Changed: []string{}}}
		lib := VersionKey{consumer.WorkspaceID, pin.LibraryPageID}
		pub, e := readLibraryPublication(ctx, tx, lib)
		if e != nil || pub.Version == 0 {
			return e
		}
		var la LibraryVersionAuthorize
		if library != nil {
			la = library(pin.LibraryPageID)
		}
		if e = libraryRead(ctx, tx, la, pub.Version, actor); e != nil {
			if errors.Is(e, ErrLibraryUnreadable) {
				return nil
			}
			return e
		}
		out.LibraryAvailable = true
		out.PublishedVersion = pub.Version
		if pub.Version == pin.LibraryVersion {
			return nil
		}
		m, e := readVersion(ctx, tx, lib, pub.Version)
		if e != nil {
			return e
		}
		next, e := libraryTokens(m)
		if e != nil {
			return e
		}
		if out.Changes, e = diffLibraryTokens(pin.Content, next); e != nil {
			return e
		}
		out.UpdateAvailable = true
		return libraryRead(ctx, tx, la, pub.Version, actor)
	})
	if err != nil {
		return LibraryUpdate{}, err
	}
	return
}

// diffLibraryTokens lists leaf paths that a library update adds, removes or
// changes. Paths are dot-separated; unusual key segments are quoted.
func diffLibraryTokens(from, to []byte) (LibraryChanges, error) {
	a, b := map[string]json.RawMessage{}, map[string]json.RawMessage{}
	if err := flattenLibraryTokens(from, "", a); err != nil {
		return LibraryChanges{}, err
	}
	if err := flattenLibraryTokens(to, "", b); err != nil {
		return LibraryChanges{}, err
	}
	out := LibraryChanges{Added: []string{}, Removed: []string{}, Changed: []string{}}
	for k, v := range b {
		if old, ok := a[k]; !ok {
			out.Added = append(out.Added, k)
		} else if !bytes.Equal(old, v) {
			out.Changed = append(out.Changed, k)
		}
	}
	for k := range a {
		if _, ok := b[k]; !ok {
			out.Removed = append(out.Removed, k)
		}
	}
	for _, list := range []*[]string{&out.Added, &out.Removed, &out.Changed} {
		sort.Strings(*list)
		if len(*list) > maxLibraryChanges {
			*list = (*list)[:maxLibraryChanges]
			out.Truncated = true
		}
	}
	return out, nil
}

func flattenLibraryTokens(raw []byte, prefix string, out map[string]json.RawMessage) error {
	var obj map[string]json.RawMessage
	if trimmed := bytes.TrimSpace(raw); len(trimmed) > 0 && trimmed[0] == '{' {
		if err := json.Unmarshal(trimmed, &obj); err != nil {
			return fmt.Errorf("%w: %v", ErrVersionInvalid, err)
		}
		for k, v := range obj {
			segment := k
			if k == "" || strings.ContainsAny(k, ".\\\"") || strings.TrimSpace(k) != k {
				segment = strconv.Quote(k)
			}
			path := segment
			if prefix != "" {
				path = prefix + "." + segment
			}
			if err := flattenLibraryTokens(v, path, out); err != nil {
				return err
			}
		}
		return nil
	}
	// Compact the leaf so formatting alone is not a change.
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		return fmt.Errorf("%w: %v", ErrVersionInvalid, err)
	}
	out[prefix] = buf.Bytes()
	return nil
}
