package access

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"slices"
	"time"

	"github.com/crewship-ai/crewship/internal/tsformat"
)

const MaxProjectFileBytes = 1 << 20
const MaxProjectSnapshotBytes = 16 << 20

// ProjectFileVersion exposes resource metadata, never uploader identity or a
// host storage path. Only a live head version may be selected or downloaded.
type ProjectFileVersion struct {
	ID        string `json:"version_id"`
	FileID    string `json:"file_id"`
	ProjectID string `json:"project_id"`
	Name      string `json:"name"`
	Revision  int64  `json:"revision"`
	Size      int64  `json:"size_bytes"`
	SHA256    string `json:"sha256"`
	CreatedAt string `json:"created_at"`
}

type ProjectFileWrite struct {
	FileID, Name     string
	ExpectedRevision int64
}
type FrozenProjectInput struct {
	Version ProjectFileVersion
	Content []byte `json:"-"`
}

func projectWriter(ctx context.Context, q queryer, user, workspace, project string) error {
	var role string
	if q.QueryRowContext(ctx, `SELECT role FROM workspace_members WHERE user_id=? AND workspace_id=?`, user, workspace).Scan(&role) != nil || !slices.Contains([]string{"OWNER", "ADMIN", "MANAGER"}, role) {
		return ErrDenied
	}
	_, err := check(ctx, q, user, workspace, Right{"project", project, "write"})
	return err
}

// PutProjectFile atomically replaces a live head and retires its bytes while
// retaining immutable version provenance. ExpectedRevision=0 creates a new file.
func (s Store) PutProjectFile(ctx context.Context, user, workspace, project string, in ProjectFileWrite, content []byte) (ProjectFileVersion, error) {
	if s.DB == nil || !validFileName(in.Name) || len(content) > MaxProjectFileBytes || in.ExpectedRevision < 0 || (in.FileID == "") != (in.ExpectedRevision == 0) {
		return ProjectFileVersion{}, ErrDenied
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return ProjectFileVersion{}, err
	}
	defer tx.Rollback()
	// Lock before checking current authority and aggregate capacity.
	if _, err = tx.ExecContext(ctx, `UPDATE projects SET name=name WHERE id=? AND workspace_id=?`, project, workspace); err != nil {
		return ProjectFileVersion{}, err
	}
	if err = projectWriter(ctx, tx, user, workspace, project); err != nil {
		return ProjectFileVersion{}, err
	}
	now := tsformat.Format(time.Now())
	file := in.FileID
	revision := in.ExpectedRevision + 1
	if file == "" {
		file = randomID()
	} else {
		var old, name string
		var current int64
		if err = tx.QueryRowContext(ctx, `SELECT head_version_id,name,revision FROM project_files WHERE id=? AND workspace_id=? AND project_id=? AND retired_at IS NULL`, file, workspace, project).Scan(&old, &name, &current); err != nil {
			return ProjectFileVersion{}, ErrDenied
		}
		if current != in.ExpectedRevision || name != in.Name {
			return ProjectFileVersion{}, ErrConflict
		}
		if _, err = tx.ExecContext(ctx, `UPDATE project_file_versions SET retired_at=? WHERE id=? AND retired_at IS NULL`, now, old); err != nil {
			return ProjectFileVersion{}, err
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM project_file_blobs WHERE version_id=?`, old); err != nil {
			return ProjectFileVersion{}, err
		}
	}
	h := sha256.Sum256(content)
	v := ProjectFileVersion{randomID(), file, project, in.Name, revision, int64(len(content)), hex.EncodeToString(h[:]), now}
	if in.FileID == "" {
		_, err = tx.ExecContext(ctx, `INSERT INTO project_files(id,workspace_id,project_id,name,revision,head_version_id,created_at) VALUES(?,?,?,?,?,?,?)`, file, workspace, project, in.Name, revision, v.ID, now)
	} else {
		_, err = tx.ExecContext(ctx, `UPDATE project_files SET revision=?,head_version_id=? WHERE id=? AND revision=? AND retired_at IS NULL`, revision, v.ID, file, in.ExpectedRevision)
	}
	if err != nil {
		return ProjectFileVersion{}, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO project_file_versions(id,workspace_id,project_id,file_id,name,revision,size_bytes,sha256,created_at) VALUES(?,?,?,?,?,?,?,?,?)`, v.ID, workspace, project, file, v.Name, revision, v.Size, v.SHA256, now); err != nil {
		return ProjectFileVersion{}, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO project_file_blobs(version_id,workspace_id,project_id,content_base64) VALUES(?,?,?,?)`, v.ID, workspace, project, base64.StdEncoding.EncodeToString(content)); err != nil {
		return ProjectFileVersion{}, err
	}
	if err = projectWriter(ctx, tx, user, workspace, project); err != nil {
		return ProjectFileVersion{}, err
	}
	if err = tx.Commit(); err != nil {
		return ProjectFileVersion{}, err
	}
	return v, nil
}

func (s Store) RetireProjectFile(ctx context.Context, user, workspace, project, file string, expected int64) error {
	if s.DB == nil || expected < 1 {
		return ErrDenied
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE projects SET name=name WHERE id=? AND workspace_id=?`, project, workspace); err != nil {
		return err
	}
	if err = projectWriter(ctx, tx, user, workspace, project); err != nil {
		return err
	}
	var version string
	if err = tx.QueryRowContext(ctx, `SELECT head_version_id FROM project_files WHERE id=? AND workspace_id=? AND project_id=? AND revision=? AND retired_at IS NULL`, file, workspace, project, expected).Scan(&version); err != nil {
		return ErrConflict
	}
	now := tsformat.Format(time.Now())
	if _, err = tx.ExecContext(ctx, `UPDATE project_files SET revision=revision+1,retired_at=? WHERE id=?`, now, file); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE project_file_versions SET retired_at=? WHERE id=? AND retired_at IS NULL`, now, version); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM project_file_blobs WHERE version_id=?`, version); err != nil {
		return err
	}
	return tx.Commit()
}

func readProjectVersion(ctx context.Context, q queryer, user, workspace, project, id string, bytes bool) (ProjectFileVersion, []byte, error) {
	if _, err := check(ctx, q, user, workspace, Right{"project", project, "read"}); err != nil {
		return ProjectFileVersion{}, nil, err
	}
	var v ProjectFileVersion
	var encoded string
	column := "''"
	if bytes {
		column = "b.content_base64"
	}
	err := q.QueryRowContext(ctx, `SELECT v.id,v.file_id,v.project_id,v.name,v.revision,v.size_bytes,v.sha256,v.created_at,`+column+`
 FROM project_file_versions v JOIN project_files f ON f.id=v.file_id AND f.head_version_id=v.id AND f.workspace_id=v.workspace_id AND f.project_id=v.project_id AND f.name=v.name AND f.revision=v.revision AND f.retired_at IS NULL
 JOIN project_file_blobs b ON b.version_id=v.id AND b.workspace_id=v.workspace_id AND b.project_id=v.project_id WHERE v.id=? AND v.workspace_id=? AND v.project_id=? AND v.retired_at IS NULL`, id, workspace, project).Scan(&v.ID, &v.FileID, &v.ProjectID, &v.Name, &v.Revision, &v.Size, &v.SHA256, &v.CreatedAt, &encoded)
	if err != nil {
		return ProjectFileVersion{}, nil, ErrDenied
	}
	if !validFileName(v.Name) || v.Size < 0 || v.Size > MaxProjectFileBytes {
		return ProjectFileVersion{}, nil, ErrDenied
	}
	if !bytes {
		return v, nil, nil
	}
	content, err := base64.StdEncoding.Strict().DecodeString(encoded)
	h := sha256.Sum256(content)
	if err != nil || int64(len(content)) != v.Size || hex.EncodeToString(h[:]) != v.SHA256 {
		return ProjectFileVersion{}, nil, ErrDenied
	}
	return v, content, nil
}

func (s Store) ReadProjectFile(ctx context.Context, user, workspace, project, id string) (ProjectFileVersion, []byte, error) {
	if s.DB == nil {
		return ProjectFileVersion{}, nil, ErrDenied
	}
	return readProjectVersion(ctx, s.DB, user, workspace, project, id, true)
}
func (s Store) CheckProjectFile(ctx context.Context, user, workspace, project, id string) error {
	if s.DB == nil {
		return ErrDenied
	}
	_, _, err := readProjectVersion(ctx, s.DB, user, workspace, project, id, false)
	return err
}
func (s Store) ProjectFiles(ctx context.Context, user, workspace, project string) ([]ProjectFileVersion, error) {
	if s.DB == nil {
		return nil, ErrDenied
	}
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err = check(ctx, tx, user, workspace, Right{"project", project, "read"}); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT head_version_id FROM project_files WHERE workspace_id=? AND project_id=? AND retired_at IS NULL ORDER BY name LIMIT 256`, workspace, project)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			_ = rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return nil, err
	}
	versions := []ProjectFileVersion{}
	for _, id := range ids {
		v, _, e := readProjectVersion(ctx, tx, user, workspace, project, id, false)
		if e != nil {
			return nil, e
		}
		versions = append(versions, v)
	}
	return versions, tx.Commit()
}

func selectedIDs(ids []string) bool {
	if len(ids) > 16 {
		return false
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if id == "" || len(id) > 96 || seen[id] {
			return false
		}
		for _, r := range id {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
				return false
			}
		}
		seen[id] = true
	}
	return true
}

// ProjectFileRights resolves explicit IDs before admission. No task content or
// caller path can select a filesystem capability.
func (s Store) ProjectFileRights(ctx context.Context, user, workspace string, ids []string) ([]Right, error) {
	if s.DB == nil || !selectedIDs(ids) {
		return nil, ErrDenied
	}
	rights := []Right{}
	var total int64
	for _, id := range ids {
		var project string
		if s.DB.QueryRowContext(ctx, `SELECT project_id FROM project_file_versions WHERE id=? AND workspace_id=?`, id, workspace).Scan(&project) != nil {
			return nil, ErrDenied
		}
		v, _, err := readProjectVersion(ctx, s.DB, user, workspace, project, id, false)
		if err != nil {
			return nil, err
		}
		total += v.Size
		r := Right{"project", project, "read"}
		if !slices.Contains(rights, r) {
			rights = append(rights, r)
		}
	}
	if total > MaxProjectSnapshotBytes {
		return nil, ErrDenied
	}
	return rights, nil
}

// BindProjectFileInputs freezes provenance before any context or model use.
// A selection is written once; replacing or extending it is denied.
func (s Store) BindProjectFileInputs(ctx context.Context, handle string, ids []string) error {
	return s.bindProjectFileInputs(ctx, digest(handle), true, nil, ids)
}

// BindProjectFileInputsForAttempt is for the trusted admission builder, which
// receives the server-resolved attempt before its opaque handle is returned.
// Public handlers must never reconstruct this argument from a request body.
func (s Store) BindProjectFileInputsForAttempt(ctx context.Context, attempt Attempt, ids []string) error {
	return s.bindProjectFileInputs(ctx, attempt.ID, false, &attempt, ids)
}

func (s Store) bindProjectFileInputs(ctx context.Context, key string, byHandle bool, want *Attempt, ids []string) error {
	if s.DB == nil || !selectedIDs(ids) {
		return ErrDenied
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	column := "id"
	if byHandle {
		column = "handle_hash"
	}
	if _, err = tx.ExecContext(ctx, `UPDATE access_attempts SET completed_at=completed_at WHERE `+column+`=?`, key); err != nil {
		return err
	}
	a, err := resolveState(ctx, tx, key, byHandle, map[string]bool{}, false)
	if err != nil {
		return err
	}
	if want != nil && (a.Scope != want.Scope || a.Generation != want.Generation || a.Principal != want.Principal || a.Workspace != want.Workspace || a.Agent != want.Agent || a.Chat != want.Chat) {
		return ErrDenied
	}
	var sealed bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM restricted_launches WHERE attempt_id=?) OR EXISTS(SELECT 1 FROM restricted_native_sessions WHERE attempt_id=?) OR EXISTS(SELECT 1 FROM access_context_dependencies WHERE attempt_id=?) OR EXISTS(SELECT 1 FROM access_context WHERE attempt_id=?)`, a.ID, a.ID, a.ID, a.ID).Scan(&sealed); err != nil {
		return err
	}
	if sealed {
		return ErrDenied
	}
	var total int64
	var count int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM attempt_project_inputs WHERE attempt_id=?`, a.ID).Scan(&count); err != nil {
		return err
	}
	if count != 0 {
		return ErrConflict
	}
	for _, id := range ids {
		var project string
		if tx.QueryRowContext(ctx, `SELECT project_id FROM project_file_versions WHERE id=? AND workspace_id=?`, id, a.Workspace).Scan(&project) != nil {
			return ErrDenied
		}
		if !slices.Contains(a.Rights, Right{"project", project, "read"}) {
			return ErrDenied
		}
		v, _, err := readProjectVersion(ctx, tx, a.Principal, a.Workspace, project, id, false)
		if err != nil {
			return err
		}
		total += v.Size
		if _, err = tx.ExecContext(ctx, `INSERT INTO attempt_project_inputs(attempt_id,version_id,workspace_id,project_id,file_id,scope,file_revision,sha256) VALUES(?,?,?,?,?,?,?,?)`, a.ID, v.ID, a.Workspace, project, v.FileID, a.Scope, v.Revision, v.SHA256); err != nil {
			return err
		}
	}
	if total > MaxProjectSnapshotBytes {
		return ErrDenied
	}
	return tx.Commit()
}

func checkProjectInputs(ctx context.Context, q queryer, a Attempt) error {
	var bad bool
	rights, _ := json.Marshal(a.Rights)
	err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM attempt_project_inputs i
 LEFT JOIN project_file_versions v ON v.id=i.version_id LEFT JOIN project_files f ON f.id=i.file_id LEFT JOIN project_file_blobs b ON b.version_id=i.version_id
 WHERE i.attempt_id=? AND (i.scope<>? OR i.workspace_id<>? OR v.id IS NULL OR b.version_id IS NULL OR v.retired_at IS NOT NULL
 OR v.workspace_id<>i.workspace_id OR v.project_id<>i.project_id OR v.file_id<>i.file_id OR v.revision<>i.file_revision
 OR b.workspace_id<>i.workspace_id OR b.project_id<>i.project_id OR f.workspace_id<>i.workspace_id OR f.project_id<>i.project_id OR f.name<>v.name
 OR f.id IS NULL OR f.retired_at IS NOT NULL OR f.head_version_id<>i.version_id OR f.revision<>i.file_revision OR v.sha256<>i.sha256
 OR NOT EXISTS(SELECT 1 FROM json_each(?) j WHERE json_extract(j.value,'$.kind')='project' AND json_extract(j.value,'$.id')=i.project_id AND json_extract(j.value,'$.operation')='read')))`, a.ID, a.Scope, a.Workspace, string(rights)).Scan(&bad)
	if err != nil {
		return err
	}
	if bad {
		return ErrDenied
	}
	return nil
}

// FrozenProjectInputs is a host-only materializer. Every call rechecks the live
// attempt, current source heads and hashes; completed handles cannot launch.
func (s Store) FrozenProjectInputs(ctx context.Context, handle string) ([]FrozenProjectInput, error) {
	return s.frozenProjectInputs(ctx, digest(handle), true, nil)
}

// FrozenProjectInputsForAttempt is the host admission/catalog projection.
func (s Store) FrozenProjectInputsForAttempt(ctx context.Context, attempt Attempt) ([]FrozenProjectInput, error) {
	return s.frozenProjectInputs(ctx, attempt.ID, false, &attempt)
}

// ProjectInputVersionsForAttempt is the host-only immutable metadata projection
// used by runtime plan resolution. It avoids repeatedly copying source bytes.
func (s Store) ProjectInputVersionsForAttempt(ctx context.Context, attempt Attempt) ([]ProjectFileVersion, error) {
	if s.DB == nil {
		return nil, ErrDenied
	}
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	a, err := resolveState(ctx, tx, attempt.ID, false, map[string]bool{}, false)
	if err != nil {
		return nil, err
	}
	if a.Scope != attempt.Scope || a.Generation != attempt.Generation || a.Principal != attempt.Principal || a.Workspace != attempt.Workspace || a.Agent != attempt.Agent || a.Chat != attempt.Chat {
		return nil, ErrDenied
	}
	rows, err := tx.QueryContext(ctx, `SELECT v.id,v.file_id,v.project_id,v.name,v.revision,v.size_bytes,v.sha256,v.created_at FROM attempt_project_inputs i JOIN project_file_versions v ON v.id=i.version_id WHERE i.attempt_id=? ORDER BY v.id LIMIT 17`, a.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	versions := []ProjectFileVersion{}
	for rows.Next() {
		var v ProjectFileVersion
		if err := rows.Scan(&v.ID, &v.FileID, &v.ProjectID, &v.Name, &v.Revision, &v.Size, &v.SHA256, &v.CreatedAt); err != nil {
			return nil, err
		}
		versions = append(versions, v)
	}
	if err := rows.Err(); err != nil || len(versions) > 16 {
		return nil, ErrDenied
	}
	return versions, nil
}

func (s Store) frozenProjectInputs(ctx context.Context, key string, byHandle bool, want *Attempt) ([]FrozenProjectInput, error) {
	if s.DB == nil {
		return nil, ErrDenied
	}
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	a, err := resolveState(ctx, tx, key, byHandle, map[string]bool{}, false)
	if err != nil {
		return nil, err
	}
	if want != nil && (a.Scope != want.Scope || a.Generation != want.Generation || a.Principal != want.Principal || a.Workspace != want.Workspace || a.Agent != want.Agent || a.Chat != want.Chat) {
		return nil, ErrDenied
	}
	rows, err := tx.QueryContext(ctx, `SELECT version_id,project_id FROM attempt_project_inputs WHERE attempt_id=? ORDER BY version_id LIMIT 17`, a.ID)
	if err != nil {
		return nil, err
	}
	type inputKey struct{ id, project string }
	var keys []inputKey
	for rows.Next() {
		var k inputKey
		if err = rows.Scan(&k.id, &k.project); err != nil {
			_ = rows.Close()
			return nil, err
		}
		keys = append(keys, k)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return nil, err
	}
	if len(keys) > 16 {
		return nil, ErrDenied
	}
	inputs := []FrozenProjectInput{}
	var total int
	for _, k := range keys {
		v, b, e := readProjectVersion(ctx, tx, a.Principal, a.Workspace, k.project, k.id, true)
		if e != nil {
			return nil, e
		}
		total += len(b)
		inputs = append(inputs, FrozenProjectInput{v, b})
	}
	if total > MaxProjectSnapshotBytes {
		return nil, ErrDenied
	}
	return inputs, tx.Commit()
}
