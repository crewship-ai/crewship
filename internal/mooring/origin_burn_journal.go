package mooring

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
)

const originBurnFilename = "origin-burn-v1.log"
const maxOriginBurnBytes = 8 << 20
const maxOriginBurnLine = 2048

var ErrOriginBurnJournal = errors.New("origin burn journal unavailable or unqualified")

// OriginBurnData is bounded correlation data, NOT author admission, a reserved
// origin grant or proof that a restored file covers every historical burn.
// BindingDigest must contain no raw bearer or document content.
type OriginBurnData struct {
	Origin        string `json:"origin"`
	SourceID      string `json:"source_id"`
	BindingDigest string `json:"binding_digest"`
}

type originBurnHeader struct {
	Format    string `json:"format"`
	LineageID string `json:"lineage_id"`
	JournalID string `json:"journal_id"`
	Digest    string `json:"digest"`
}
type originBurnRecord struct {
	Sequence uint32 `json:"sequence"`
	OriginBurnData
	Previous string `json:"previous"`
	Digest   string `json:"digest"`
}
type originBurnFile interface {
	io.ReaderAt
	Write([]byte) (int, error)
	Sync() error
	Stat() (os.FileInfo, error)
	Close() error
}

// OriginBurnJournal is an inactive local append/fsync data provider. No route
// or issuer opens it. SQL/portable restore cannot recreate this private handle.
// Valid complete-prefix truncation or rollback cannot be detected without an
// independent anchor: this provider is NOT physical/offhost recovery coverage.
// No method truncates, repairs, evicts or recycles a recorded origin.
type OriginBurnJournal struct {
	mu       sync.Mutex
	self     *OriginBurnJournal
	root     *os.Root
	file     originBurnFile
	identity os.FileInfo
	header   originBurnHeader
	records  []originBurnRecord
	size     int64
	fileHash [32]byte
	poisoned bool
	closed   bool
}

// CreateFreshOriginBurnJournal is an explicit fresh-file operation, never a
// missing-history fallback. The existing absolute root is trusted application config,
// not request JSON. Creation does not prove a fresh/unissued document lineage.
func CreateFreshOriginBurnJournal(ctx context.Context, enabled bool, dataRoot, lineage string) (*OriginBurnJournal, error) {
	if !enabled || ctx == nil || ctx.Err() != nil || !originBurnSupported || !filepath.IsAbs(dataRoot) || len(lineage) > 800 || !ValidWireIdentifier(lineage) {
		return nil, ErrOriginBurnJournal
	}
	root, err := os.OpenRoot(dataRoot)
	if err != nil {
		return nil, ErrOriginBurnJournal
	}
	if info, err := root.Stat("."); err != nil || !info.IsDir() || info.Mode().Perm()&0o022 != 0 {
		_ = root.Close()
		return nil, ErrOriginBurnJournal
	}
	f, err := openOriginBurnFile(root, os.O_RDWR|os.O_CREATE|os.O_EXCL|os.O_APPEND)
	if err != nil {
		_ = root.Close()
		return nil, ErrOriginBurnJournal
	}
	fail := func() (*OriginBurnJournal, error) { _ = f.Close(); _ = root.Close(); return nil, ErrOriginBurnJournal }
	if lockOriginBurnFile(f) != nil {
		return fail()
	}
	var nonce [32]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		return fail()
	}
	header := originBurnHeader{Format: "mooring.origin-burn.v1", LineageID: lineage, JournalID: hex.EncodeToString(nonce[:])}
	header.Digest = originBurnHash("mooring.origin-burn.header.v1", header)
	raw, _ := json.Marshal(header)
	raw = append(raw, '\n')
	if len(raw) > maxOriginBurnLine || ctx.Err() != nil {
		return fail()
	}
	if n, err := f.Write(raw); err != nil || n != len(raw) || f.Sync() != nil {
		return fail()
	}
	// The new name, not merely its file data, must survive acknowledged creation.
	dir, err := root.Open(".")
	if err != nil {
		return fail()
	}
	err = dir.Sync()
	closeErr := dir.Close()
	if err != nil || closeErr != nil || ctx.Err() != nil {
		return fail()
	}
	info, err := f.Stat()
	if err != nil || !originBurnSafeFile(info) {
		return fail()
	}
	j := &OriginBurnJournal{root: root, file: f, identity: info, header: header, size: int64(len(raw)), fileHash: sha256.Sum256(raw)}
	j.self = j
	if j.checkFile() != nil {
		return fail()
	}
	return j, nil
}

// OpenOriginBurnJournal never creates missing data or guesses a journal ID.
// expectedID is correlation from a separately trusted host record, NOT recovery
// coverage. An intact old prefix may still be incomplete after physical rollback.
func OpenOriginBurnJournal(ctx context.Context, enabled bool, dataRoot, lineage, expectedID string) (*OriginBurnJournal, error) {
	if !enabled || ctx == nil || ctx.Err() != nil || !originBurnSupported || !filepath.IsAbs(dataRoot) || len(lineage) > 800 || !ValidWireIdentifier(lineage) || !originBurnHex(expectedID, 64) {
		return nil, ErrOriginBurnJournal
	}
	root, err := os.OpenRoot(dataRoot)
	if err != nil {
		return nil, ErrOriginBurnJournal
	}
	if info, err := root.Stat("."); err != nil || !info.IsDir() || info.Mode().Perm()&0o022 != 0 {
		_ = root.Close()
		return nil, ErrOriginBurnJournal
	}
	pathInfo, err := root.Lstat(originBurnFilename)
	if err != nil || !originBurnSafeFile(pathInfo) {
		_ = root.Close()
		return nil, ErrOriginBurnJournal
	}
	f, err := openOriginBurnFile(root, os.O_RDWR|os.O_APPEND)
	if err != nil {
		_ = root.Close()
		return nil, ErrOriginBurnJournal
	}
	fail := func() (*OriginBurnJournal, error) { _ = f.Close(); _ = root.Close(); return nil, ErrOriginBurnJournal }
	if lockOriginBurnFile(f) != nil {
		return fail()
	}
	info, err := f.Stat()
	if err != nil || !originBurnSafeFile(info) || info.Size() < 1 || info.Size() > maxOriginBurnBytes {
		return fail()
	}
	var last [1]byte
	if _, err := f.ReadAt(last[:], info.Size()-1); err != nil || last[0] != '\n' {
		return fail()
	}
	parsedHash := sha256.New()
	scanner := bufio.NewScanner(io.TeeReader(io.NewSectionReader(f, 0, info.Size()), parsedHash))
	scanner.Buffer(make([]byte, 512), maxOriginBurnLine)
	var header originBurnHeader
	if !scanner.Scan() || !originBurnDecode(scanner.Bytes(), &header) || header.Format != "mooring.origin-burn.v1" || header.LineageID != lineage || header.JournalID != expectedID {
		return fail()
	}
	digest := header.Digest
	header.Digest = ""
	if digest != originBurnHash("mooring.origin-burn.header.v1", header) {
		return fail()
	}
	header.Digest = digest
	j := &OriginBurnJournal{root: root, file: f, identity: info, header: header, size: info.Size()}
	j.self = j
	for scanner.Scan() {
		if ctx.Err() != nil || len(j.records) >= MaxOriginInventoryEntries {
			return fail()
		}
		var record originBurnRecord
		if !originBurnDecode(scanner.Bytes(), &record) || j.validateNext(record) != nil {
			return fail()
		}
		j.records = append(j.records, record)
	}
	copy(j.fileHash[:], parsedHash.Sum(nil))
	if scanner.Err() != nil || ctx.Err() != nil || j.checkFile() != nil || f.Sync() != nil || originBurnSyncRoot(root) != nil {
		return fail()
	}
	return j, nil
}

func originBurnHash(domain string, value any) string {
	raw, _ := json.Marshal(value)
	h := sha256.New()
	_, _ = h.Write([]byte(domain + "\x00"))
	_, _ = h.Write(raw)
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}
func originBurnHex(value string, length int) bool {
	if len(value) != length {
		return false
	}
	for _, c := range value {
		if c < '0' || c > '9' && c < 'a' || c > 'f' {
			return false
		}
	}
	return true
}
func originBurnDigest(value string) bool {
	return len(value) == 71 && value[:7] == "sha256:" && originBurnHex(value[7:], 64)
}
func originBurnValidData(data OriginBurnData) bool {
	_, err := originDataCounter(data.Origin, MaxIssuedOrigin, false)
	return err == nil && len(data.SourceID) <= 800 && ValidWireIdentifier(data.SourceID) && originBurnDigest(data.BindingDigest)
}
func originBurnDecode(raw []byte, target any) bool {
	if len(raw) == 0 || len(raw) >= maxOriginBurnLine {
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(target) != nil {
		return false
	}
	canonical, err := json.Marshal(target)
	return err == nil && bytes.Equal(raw, canonical)
}
func (j *OriginBurnJournal) tail() string {
	if len(j.records) == 0 {
		return j.header.Digest
	}
	return j.records[len(j.records)-1].Digest
}
func (j *OriginBurnJournal) validateNext(record originBurnRecord) error {
	if !originBurnValidData(record.OriginBurnData) || record.Sequence != uint32(len(j.records)+1) || record.Previous != j.tail() {
		return ErrOriginBurnJournal
	}
	digest := record.Digest
	record.Digest = ""
	if digest != originBurnHash("mooring.origin-burn.record.v1", record) {
		return ErrOriginBurnJournal
	}
	for _, previous := range j.records {
		if previous.Origin == record.Origin || previous.SourceID == record.SourceID {
			return ErrOriginBurnJournal
		}
	}
	return nil
}
func originBurnSyncRoot(root *os.Root) error {
	dir, err := root.Open(".")
	if err != nil {
		return err
	}
	return errors.Join(dir.Sync(), dir.Close())
}
func (j *OriginBurnJournal) fileDigest() ([32]byte, error) {
	var empty [32]byte
	info, err := j.file.Stat()
	if err != nil || !originBurnSafeFile(info) || !os.SameFile(j.identity, info) || info.Size() != j.size || j.size < 1 || j.size > maxOriginBurnBytes {
		return empty, ErrOriginBurnJournal
	}
	path, err := j.root.Lstat(originBurnFilename)
	if err != nil || !originBurnSafeFile(path) || !os.SameFile(info, path) {
		return empty, ErrOriginBurnJournal
	}
	hash := sha256.New()
	var buffer [4096]byte
	n, err := io.CopyBuffer(hash, io.NewSectionReader(j.file, 0, j.size), buffer[:])
	if err != nil || n != j.size {
		return empty, ErrOriginBurnJournal
	}
	copy(empty[:], hash.Sum(nil))
	return empty, nil
}
func (j *OriginBurnJournal) checkFile() error {
	digest, err := j.fileDigest()
	if err != nil || digest != j.fileHash {
		return ErrOriginBurnJournal
	}
	return nil
}
func (j *OriginBurnJournal) verifyFile() error {
	if err := j.checkFile(); err != nil {
		j.poisoned = true
		return err
	}
	return nil
}
func (j *OriginBurnJournal) usable(ctx context.Context) bool {
	return ctx != nil && ctx.Err() == nil && j != nil && j.self == j && !j.closed && !j.poisoned
}

// IdentityData is a copy of correlation strings, never an admission or coverage
// certificate. Callers cannot use it to resurrect a source after cold recovery.
func (j *OriginBurnJournal) IdentityData(ctx context.Context) (lineage, journal string, err error) {
	if j == nil || j.self != j {
		return "", "", ErrOriginBurnJournal
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if !j.usable(ctx) || j.verifyFile() != nil {
		return "", "", ErrOriginBurnJournal
	}
	return j.header.LineageID, j.header.JournalID, nil
}

// EntriesData returns owned data, including every issued-unused/rejected burn.
// Neither this slice nor an empty valid journal proves complete lineage history.
func (j *OriginBurnJournal) EntriesData(ctx context.Context) ([]OriginBurnData, error) {
	if j == nil || j.self != j {
		return nil, ErrOriginBurnJournal
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if !j.usable(ctx) || j.verifyFile() != nil {
		return nil, ErrOriginBurnJournal
	}
	result := make([]OriginBurnData, len(j.records))
	for i, record := range j.records {
		result[i] = record.OriginBurnData
	}
	return result, nil
}

// AppendBurnData acknowledges only after the complete bounded record is fsynced.
// Exact retries return the original digest. Rebinding either identity refuses.
// Any write/sync uncertainty permanently poisons this instance; no caller may
// infer an unburned origin from that error or proceed to source publication.
func (j *OriginBurnJournal) AppendBurnData(ctx context.Context, data OriginBurnData) (string, error) {
	if j == nil || j.self != j {
		return "", ErrOriginBurnJournal
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if !j.usable(ctx) || !originBurnValidData(data) || j.verifyFile() != nil {
		return "", ErrOriginBurnJournal
	}
	for _, record := range j.records {
		if record.Origin == data.Origin || record.SourceID == data.SourceID {
			if record.OriginBurnData == data {
				return record.Digest, nil
			}
			return "", ErrOriginBurnJournal
		}
	}
	if len(j.records) >= MaxOriginInventoryEntries {
		return "", ErrOriginBurnJournal
	}
	record := originBurnRecord{Sequence: uint32(len(j.records) + 1), OriginBurnData: data, Previous: j.tail()}
	record.Digest = originBurnHash("mooring.origin-burn.record.v1", record)
	raw, _ := json.Marshal(record)
	raw = append(raw, '\n')
	if len(raw) > maxOriginBurnLine || j.size+int64(len(raw)) > maxOriginBurnBytes {
		return "", ErrOriginBurnJournal
	}
	expectedHash := sha256.New()
	var buffer [4096]byte
	if n, err := io.CopyBuffer(expectedHash, io.NewSectionReader(j.file, 0, j.size), buffer[:]); err != nil || n != j.size {
		j.poisoned = true
		return "", ErrOriginBurnJournal
	}
	// The second read seeds the append hash. It must still be the previously
	// verified prefix, not bytes changed between validation and preparation.
	if !bytes.Equal(expectedHash.Sum(nil), j.fileHash[:]) {
		j.poisoned = true
		return "", ErrOriginBurnJournal
	}
	_, _ = expectedHash.Write(raw)
	j.poisoned = true
	if n, err := j.file.Write(raw); err != nil || n != len(raw) || j.file.Sync() != nil {
		return "", ErrOriginBurnJournal
	}
	j.size += int64(len(raw))
	copy(j.fileHash[:], expectedHash.Sum(nil))
	if ctx.Err() != nil || j.checkFile() != nil {
		return "", ErrOriginBurnJournal
	}
	j.records = append(j.records, record)
	j.poisoned = false
	return record.Digest, nil
}

// Close releases only this handle/lock. It never deletes or truncates history.
func (j *OriginBurnJournal) Close() error {
	if j == nil || j.self != j {
		return ErrOriginBurnJournal
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.closed {
		return nil
	}
	j.closed = true
	fileErr := j.file.Close()
	rootErr := j.root.Close()
	return errors.Join(fileErr, rootErr)
}
