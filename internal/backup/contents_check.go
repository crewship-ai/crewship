package backup

// Proof level 2 — contents checked: the bundle is decrypted, every section
// is read to the end, and what it holds is compared with what its manifest
// says it holds (table row counts, file digests, blob counts). Nothing is
// restored. The key is used for the check only; the caller never stores it.

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"filippo.io/age"
)

// ContentsCheck is the result of CheckBundleContents.
type ContentsCheck struct {
	OK bool `json:"ok"`
	// ProofLevel is 2 when the check passed, 1 when only the checksum held,
	// 0 when not even that.
	ProofLevel int    `json:"proof_level"`
	Detail     string `json:"detail"`
	Scope      Scope  `json:"scope"`
	// Entries is how many payload entries were read end to end.
	Entries int `json:"entries"`
	// Tables / Files are what was compared.
	Tables int `json:"tables"`
	Files  int `json:"files"`
	// Problems lists every mismatch; empty when OK.
	Problems []string `json:"problems"`
	// Absent names what the bundle itself recorded as missing (attachment
	// files, memory blobs, crew containers) — not a check failure, but never
	// left out of the answer.
	Absent []IncompleteItem `json:"absent"`
}

// CheckBundleContents decrypts the bundle at path with identities or
// passphrase, reads every section, and compares it with the manifest.
func CheckBundleContents(ctx context.Context, bundlePath string, identities []age.Identity, passphrase string) (*ContentsCheck, error) {
	res := &ContentsCheck{Problems: []string{}, Absent: []IncompleteItem{}}
	vr, err := Verify(ctx, bundlePath)
	if err != nil {
		return nil, err
	}
	if vr.Manifest != nil {
		res.Scope = vr.Manifest.Scope
	}
	if !vr.Valid {
		res.Detail = "checksum does not match: " + errString(vr.Err)
		res.Problems = append(res.Problems, res.Detail)
		return res, nil
	}
	res.ProofLevel = ProofChecksum
	m := vr.Manifest
	res.Absent = append(res.Absent, manifestIncomplete(m)...)
	if m.Encryption.Enabled && len(identities) == 0 && passphrase == "" {
		return nil, fmt.Errorf("backup: a contents check needs the key that opens the bundle")
	}
	_, tr, closeAll, err := openInstancePayload(bundlePath, identities, passphrase)
	if err != nil {
		return nil, err
	}
	defer closeAll()
	if m.Scope == ScopeInstance {
		err = checkInstanceContents(ctx, tr, m, filepath.Dir(bundlePath), res)
	} else {
		err = checkWorkspaceContents(ctx, tr, m, res)
	}
	if err != nil {
		return nil, err
	}
	if len(res.Problems) == 0 {
		res.OK = true
		res.ProofLevel = ProofContents
		res.Detail = fmt.Sprintf("every section read (%d entries); %d table(s) and %d file(s) match the manifest", res.Entries, res.Tables, res.Files)
		if n := len(res.Absent); n > 0 {
			res.Detail += fmt.Sprintf("; the bundle records %d kind(s) of missing content", n)
		}
	} else {
		res.Detail = fmt.Sprintf("%d mismatch(es): %s", len(res.Problems), strings.Join(res.Problems, "; "))
	}
	return res, nil
}

func errString(err error) string {
	if err == nil {
		return "unknown"
	}
	return err.Error()
}

func hashAll(r io.Reader) (string, int64, error) {
	h := sha256.New()
	n, err := io.Copy(h, r)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

func checkInstanceContents(ctx context.Context, tr *TarZstReader, m *Manifest, tmpParent string, res *ContentsCheck) error {
	inst := m.Contents.Instance
	if inst == nil {
		res.Problems = append(res.Problems, "manifest has no instance contents")
		return nil
	}
	tmp, err := os.MkdirTemp(tmpParent, ".check-")
	if err != nil {
		tmp, err = os.MkdirTemp("", "crewship-check-")
		if err != nil {
			return err
		}
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	got := map[string]map[string]IndexEntry{}
	var index *InstanceIndex
	dbSeen, kitSeen := false, false
	crews := map[string]bool{}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("backup: read payload: %w", err)
		}
		res.Entries++
		switch name := hdr.Name; {
		case name == instanceDBEntry:
			dbSeen = true
			dbPath := filepath.Join(tmp, "db.sqlite")
			sha, _, err := writeExtracted(dbPath, tr)
			if err != nil {
				return err
			}
			if sha != inst.DatabaseSHA256 {
				res.Problems = append(res.Problems, "the database snapshot does not match its recorded digest")
				continue
			}
			db, err := openSnapshot(dbPath)
			if err != nil {
				res.Problems = append(res.Problems, "the database snapshot does not open: "+err.Error())
				continue
			}
			counts, err := snapshotRowCounts(ctx, db)
			_ = db.Close()
			if err != nil {
				return err
			}
			for _, mm := range compareRowCounts(m.Contents.TableRowCounts, counts) {
				res.Problems = append(res.Problems, fmt.Sprintf("table %s: manifest %d rows, snapshot %d", mm.Table, mm.Recorded, mm.Actual))
			}
			res.Tables = len(m.Contents.TableRowCounts)
			_ = os.Remove(dbPath)
		case name == instanceIndexEntry:
			var idx InstanceIndex
			if err := json.NewDecoder(io.LimitReader(tr, maxBackupDBDumpBytes)).Decode(&idx); err != nil {
				res.Problems = append(res.Problems, "the file index does not parse")
				continue
			}
			index = &idx
		case name == RecoveryKitPath:
			kitSeen = true
			data, err := io.ReadAll(io.LimitReader(tr, 1<<20))
			if err != nil {
				return err
			}
			if _, err := ParseRecoveryKit(data); err != nil {
				res.Problems = append(res.Problems, "the recovery kit does not parse")
			}
		case strings.HasPrefix(name, instanceFilesPrefix):
			store, rel, _ := strings.Cut(strings.TrimPrefix(name, instanceFilesPrefix), "/")
			sha, n, err := hashAll(tr)
			if err != nil {
				return err
			}
			if got[store] == nil {
				got[store] = map[string]IndexEntry{}
			}
			got[store][rel] = IndexEntry{Path: rel, Size: n, SHA256: sha}
		case strings.HasPrefix(name, instanceCrewsPrefix):
			if _, _, err := hashAll(tr); err != nil {
				return err
			}
			crews[strings.TrimSuffix(strings.TrimPrefix(name, instanceCrewsPrefix), instanceCrewsSuffix)] = true
		default:
			if _, err := io.Copy(io.Discard, tr); err != nil {
				return err
			}
		}
	}
	if !dbSeen {
		res.Problems = append(res.Problems, "the payload carries no database")
	}
	if inst.RecoveryKit && !kitSeen {
		res.Problems = append(res.Problems, "the manifest says the recovery kit is included and the payload does not carry it")
	}
	for _, id := range inst.CrewArchives {
		if !crews[id] {
			res.Problems = append(res.Problems, "crew archive for workspace "+id+" is missing")
		}
	}
	if index == nil {
		res.Problems = append(res.Problems, "the payload carries no file index")
		return nil
	}
	stores := make([]string, 0, len(inst.Stores))
	for s := range inst.Stores {
		stores = append(stores, s)
	}
	sort.Strings(stores)
	for _, store := range stores {
		want := inst.Stores[store]
		entries := index.Stores[store]
		if want.IndexSHA256 != "" && StoreIndexDigest(entries) != want.IndexSHA256 {
			res.Problems = append(res.Problems, store+": the file index does not match the manifest")
		}
		if len(entries) != want.Files {
			res.Problems = append(res.Problems, fmt.Sprintf("%s: manifest %d files, index %d", store, want.Files, len(entries)))
		}
		for _, e := range entries {
			g, ok := got[store][e.Path]
			switch {
			case !ok:
				res.Problems = append(res.Problems, store+"/"+e.Path+": missing from the payload")
			case g.SHA256 != e.SHA256 || g.Size != e.Size:
				res.Problems = append(res.Problems, store+"/"+e.Path+": content does not match its digest")
			}
		}
		res.Files += len(entries)
	}
	return nil
}

func checkWorkspaceContents(ctx context.Context, tr *TarZstReader, m *Manifest, res *ContentsCheck) error {
	attachments, memoryBlobs := 0, 0
	var dump *DBDump
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("backup: read payload: %w", err)
		}
		res.Entries++
		name := strings.TrimPrefix(hdr.Name, "./")
		switch {
		case name == "db/dump.json" && hdr.Typeflag == tar.TypeReg:
			data, err := io.ReadAll(io.LimitReader(tr, maxBackupDBDumpBytes))
			if err != nil {
				return err
			}
			dump, err = UnmarshalDump(data)
			if err != nil {
				res.Problems = append(res.Problems, "the database dump does not parse")
			}
		case strings.HasPrefix(name, attachmentBlobsSectionPrefix) && hdr.Typeflag == tar.TypeReg,
			strings.HasPrefix(name, memoryBlobsSectionPrefix) && hdr.Typeflag == tar.TypeReg:
			sha, _, err := hashAll(tr)
			if err != nil {
				return err
			}
			if want := path.Base(name); sha != want {
				res.Problems = append(res.Problems, name+": content does not match its name")
			}
			if strings.HasPrefix(name, attachmentBlobsSectionPrefix) {
				attachments++
			} else {
				memoryBlobs++
			}
			res.Files++
		default:
			if _, err := io.Copy(io.Discard, tr); err != nil {
				return err
			}
		}
	}
	if dump != nil {
		for _, mm := range compareRowCounts(m.Contents.TableRowCounts, tableRowCounts(dump)) {
			res.Problems = append(res.Problems, fmt.Sprintf("table %s: manifest %d rows, dump %d", mm.Table, mm.Recorded, mm.Actual))
		}
		res.Tables = len(m.Contents.TableRowCounts)
	} else if len(m.Contents.TableRowCounts) > 0 {
		res.Problems = append(res.Problems, "the manifest records tables but the payload carries no database dump")
	}
	if attachments != m.Contents.AttachmentsIncluded {
		res.Problems = append(res.Problems, fmt.Sprintf("attachment files: manifest %d, payload %d", m.Contents.AttachmentsIncluded, attachments))
	}
	if memoryBlobs != m.Contents.MemoryBlobsIncluded {
		res.Problems = append(res.Problems, fmt.Sprintf("memory blobs: manifest %d, payload %d", m.Contents.MemoryBlobsIncluded, memoryBlobs))
	}
	return nil
}
