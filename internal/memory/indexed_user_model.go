package memory

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"os"
	"path/filepath"
)

// ReadIndexedUserModel reads the one authoritative workspace/user model,
// wherever the latest sweep placed it. Consumers must not fall back to a
// crew-local copy: it can be obsolete after a sweep, deletion or opt-out.
func ReadIndexedUserModel(ctx context.Context, db *sql.DB, base, workspaceID, userID string) (string, error) {
	if base == "" || workspaceID == "" || userID == "" {
		return "", nil
	}
	var crewID string
	err := db.QueryRowContext(ctx, `SELECT u.crew_id FROM user_models u
 JOIN crews c ON c.id=u.crew_id AND c.workspace_id=u.workspace_id
 WHERE u.workspace_id=? AND u.user_id=? AND c.deleted_at IS NULL
 AND NOT EXISTS (SELECT 1 FROM user_peer_consent p WHERE p.workspace_id=u.workspace_id AND p.user_id=u.user_id AND p.opted_out=1)`, workspaceID, userID).Scan(&crewID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if crewID == "" || filepath.Base(crewID) != crewID || crewID == "." || crewID == ".." {
		return "", errors.New("invalid model location")
	}
	root, err := os.OpenRoot(base)
	if err != nil {
		return "", err
	}
	for _, part := range []string{"crews", crewID, "shared", ".memory", "users"} {
		info, statErr := root.Lstat(part)
		if statErr != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			root.Close()
			if statErr != nil {
				return "", statErr
			}
			return "", errors.New("invalid model directory")
		}
		next, openErr := root.OpenRoot(part)
		root.Close()
		if openErr != nil {
			return "", openErr
		}
		root = next
	}
	defer root.Close()
	name := UserSlug(userID, workspaceID) + ".md"
	info, err := root.Lstat(name)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("invalid model file")
	}
	file, err := root.Open(name)
	if err != nil {
		return "", err
	}
	defer file.Close()
	body, err := io.ReadAll(io.LimitReader(file, UserModelCapBytes+1))
	if err != nil {
		return "", err
	}
	if len(body) > UserModelCapBytes {
		return "", errors.New("user model exceeds size limit")
	}
	return string(body), nil
}

// PersonalizationAllowed treats an unavailable consent store as unknown;
// prompt consumers must fail closed on its error.
func PersonalizationAllowed(ctx context.Context, db *sql.DB, workspaceID, userID string) (bool, error) {
	var optedOut bool
	err := db.QueryRowContext(ctx, "SELECT opted_out FROM user_peer_consent WHERE workspace_id=? AND user_id=?", workspaceID, userID).Scan(&optedOut)
	if errors.Is(err, sql.ErrNoRows) {
		return true, nil
	}
	return !optedOut && err == nil, err
}
