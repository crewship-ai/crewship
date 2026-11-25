package backup

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// The extracted entries contain the digest and length of the bytes actually
// written. A matching file inherited from the target is not bundle evidence.
func rebaseForceMemoryVersions(ctx context.Context, db *sql.DB, stageDir, finalMemory string, stores map[string]map[string]IndexEntry, verified map[string]int64) error {
	rows, err := db.QueryContext(ctx, `SELECT workspace_id,tier,path,sha256,bytes,payload_ref=path,
		typeof(sha256)='text' AND typeof(bytes)='integer' FROM memory_versions`)
	if err != nil {
		return err
	}
	defer rows.Close()
	// Pre-v91 databases gain an empty table in the foreground migration.
	// A missing table at this point is a broken migrated schema.
	personas := map[string]string{}
	crewOwners := map[string]string{}
	for rows.Next() {
		var workspace, tier, sourcePath, sha string
		var size int64
		var personaRef, typed bool
		if err := rows.Scan(&workspace, &tier, &sourcePath, &sha, &size, &personaRef, &typed); err != nil {
			return err
		}
		if !typed || !validSha256Hex(sha) || size < 0 {
			return errors.New("invalid memory version digest or byte count")
		}
		verified[sha] = size
		rel := "versions/" + sha[:2] + "/" + sha
		if entry, ok := stores[StoreMemory][rel]; ok {
			if entry.SHA256 != sha || entry.Size != size {
				return fmt.Errorf("memory object %s differs from its digest or byte count", sha)
			}
			continue
		}
		// Persona writers historically recorded the mutable PERSONA.md path.
		// Recover a version only when the bundled persona still has its bytes.
		outputRel := legacyPersonaEntry(sourcePath, workspace)
		entry, ok := stores[StoreOutput][outputRel]
		if tier != "persona" || !personaRef || outputRel == "" || !ok || entry.SHA256 != sha || entry.Size != size {
			return fmt.Errorf("no verified memory object for %s", sha)
		}
		if crewPath, crewScoped := strings.CutPrefix(outputRel, "crews/"); crewScoped {
			crew, _, _ := strings.Cut(crewPath, "/")
			if other, seen := crewOwners[crew]; seen && other != workspace {
				return errors.New("inconsistent workspace for legacy persona")
			}
			crewOwners[crew] = workspace
		}
		personas[sha] = entry.Path
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}

	for crew, workspace := range crewOwners {
		var owner string
		if err := db.QueryRowContext(ctx, `SELECT workspace_id FROM crews WHERE id=?`, crew).Scan(&owner); err != nil {
			return fmt.Errorf("verify legacy persona crew: %w", err)
		}
		if owner != workspace {
			return errors.New("legacy persona belongs to a different workspace")
		}
	}
	for sha, rel := range personas {
		source, err := os.OpenInRoot(RecoveredStoreDir(stageDir, StoreOutput), filepath.FromSlash(rel))
		if err != nil {
			return err
		}
		dst := filepath.Join(RecoveredStoreDir(stageDir, StoreMemory), "versions", sha[:2], sha)
		err = writeBlobDurable(ctx, dst, source, sha)
		source.Close()
		if err != nil {
			return err
		}
	}
	// All rows now name evidenced content-addressed objects. Use the final
	// published root for references persisted in SQLite.
	_, err = db.ExecContext(ctx, `UPDATE memory_versions SET payload_ref = ? || '/' || substr(sha256,1,2) || '/' || sha256`, filepath.Join(finalMemory, "versions"))
	return err
}

func verifyForceMemoryVersions(ctx context.Context, db *sql.DB, stageDir, finalMemory string, verified map[string]int64) error {
	// Check the final staged files too, once per distinct object. The index
	// proves bundle provenance; the disk read proves what the published
	// reference will actually serve.
	if len(verified) > 0 {
		root, err := os.OpenRoot(RecoveredStoreDir(stageDir, StoreMemory))
		if err != nil {
			return err
		}
		defer root.Close()
		for sha, size := range verified {
			if err := verifyStagedMemoryObject(ctx, root, sha, size); err != nil {
				return err
			}
		}
	}
	refs, err := db.QueryContext(ctx, `SELECT DISTINCT sha256, bytes, payload_ref,
		typeof(sha256)='text' AND typeof(bytes)='integer' AND typeof(payload_ref)='text' FROM memory_versions`)
	if err != nil {
		return err
	}
	defer refs.Close()
	referenced := 0
	for refs.Next() {
		var sha, ref string
		var size int64
		var typed bool
		if err := refs.Scan(&sha, &size, &ref, &typed); err != nil {
			return err
		}
		n, ok := verified[sha]
		if !typed || !ok || n != size || ref != filepath.Join(finalMemory, "versions", sha[:2], sha) {
			return errors.New("unverified memory version reference after rewrite")
		}
		referenced++
	}
	if err := refs.Err(); err != nil {
		return err
	}
	if referenced != len(verified) {
		return errors.New("verified memory version reference missing after report insertion")
	}
	return nil
}

func verifyStagedMemoryObject(ctx context.Context, root *os.Root, sha string, size int64) error {
	f, err := root.Open(filepath.Join("versions", sha[:2], sha))
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() != size {
		return fmt.Errorf("invalid staged memory object %s", sha)
	}
	h := sha256.New()
	n, err := io.Copy(h, &restoreReadGuard{ctx: ctx, r: f, b: readBudget(ctx), decoded: true})
	if err != nil {
		return err
	}
	if n != size || hex.EncodeToString(h.Sum(nil)) != sha {
		return fmt.Errorf("staged memory object %s differs from its digest or byte count", sha)
	}
	return nil
}

// Resolve only recorded persona layouts, using their suffix as a lookup in
// the extracted output index. The source host's absolute path is never opened.
func legacyPersonaEntry(path, workspace string) string {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return ""
	}
	parts := strings.Split(filepath.ToSlash(path), "/")
	n := len(parts)
	if n < 5 || parts[n-2] != ".memory" || parts[n-1] != "PERSONA.md" {
		return ""
	}
	if parts[n-5] == "crews" && parts[n-3] == "shared" {
		return strings.Join(parts[n-5:], "/")
	}
	if n >= 6 && parts[n-4] == "agents" && (parts[n-6] == "crews" || parts[n-6] == "solo" && parts[n-5] == workspace) {
		return strings.Join(parts[n-6:], "/")
	}
	return ""
}
