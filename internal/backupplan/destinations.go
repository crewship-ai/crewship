package backupplan

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/backup/offsite"
	"github.com/crewship-ai/crewship/internal/encryption"
)

// DestinationLocal is the plan destination every bundle has: this server.
const DestinationLocal = "local"

// Destination is one off-site store an instance admin added
// (GET/POST …/backups/destinations). The secret access key is stored as a
// vault envelope and never returned.
type Destination struct {
	ID                  string  `json:"id"`
	Name                string  `json:"name"`
	Kind                string  `json:"kind"`
	Endpoint            string  `json:"endpoint"`
	Region              string  `json:"region"`
	Bucket              string  `json:"bucket"`
	Prefix              string  `json:"prefix"`
	AccessKeyID         string  `json:"access_key_id"`
	PathStyle           bool    `json:"path_style"`
	AllowPrivateNetwork bool    `json:"allow_private_network"`
	LastTestAt          *string `json:"last_test_at"`
	LastTestError       *string `json:"last_test_error"`
	CreatedBy           *string `json:"created_by"`
	CreatedAt           string  `json:"created_at"`
	// Copies is how many bundles have a verified copy here, and CopyBytes
	// their total size.
	Copies    int   `json:"copies"`
	CopyBytes int64 `json:"copy_bytes"`
	// LastVerifiedAt is the newest verified copy.
	LastVerifiedAt *string `json:"last_verified_at"`
	// UsedBy names the plans that copy here.
	UsedBy []string `json:"used_by"`

	secretEnc string
}

// ErrDestinationInUse: a plan still copies to the destination.
var ErrDestinationInUse = errors.New("backupplan: the destination is used by a plan")

// S3Config is the destination's transfer configuration with its secret.
func (d *Destination) S3Config(secret string) offsite.S3Config {
	return offsite.S3Config{
		Endpoint: d.Endpoint, Region: d.Region, Bucket: d.Bucket, Prefix: d.Prefix, AccessKeyID: d.AccessKeyID,
		SecretAccessKey: secret, PathStyle: d.PathStyle, AllowPrivateNetwork: d.AllowPrivateNetwork,
	}
}

const destinationColumns = `id, name, kind, endpoint, region, bucket, prefix, access_key_id, secret_enc, path_style,
	allow_private_network, last_test_at, last_test_error, created_by, created_at`

func scanDestination(s scanner) (*Destination, error) {
	var d Destination
	var ps, ap int
	var testAt, testErr, by sql.NullString
	if err := s.Scan(&d.ID, &d.Name, &d.Kind, &d.Endpoint, &d.Region, &d.Bucket, &d.Prefix, &d.AccessKeyID, &d.secretEnc,
		&ps, &ap, &testAt, &testErr, &by, &d.CreatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	d.PathStyle, d.AllowPrivateNetwork = ps == 1, ap == 1
	d.LastTestAt, d.LastTestError, d.CreatedBy = strPtr(testAt), strPtr(testErr), strPtr(by)
	d.UsedBy = []string{}
	return &d, nil
}

// ListDestinations returns every off-site destination with its copy counts
// and the plans that use it. A schema without the table reads as none.
func ListDestinations(ctx context.Context, db *sql.DB) ([]Destination, error) {
	rows, err := db.QueryContext(ctx, `SELECT `+destinationColumns+` FROM backup_offsite_destinations ORDER BY created_at, id`)
	if err != nil {
		if strings.Contains(err.Error(), "no such table") {
			return []Destination{}, nil
		}
		return nil, err
	}
	out := []Destination{}
	for rows.Next() {
		d, err := scanDestination(rows)
		if err != nil {
			_ = rows.Close()
			return nil, err
		}
		out = append(out, *d)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	plans, _ := ListPlans(ctx, db)
	for i := range out {
		d := &out[i]
		var last sql.NullString
		_ = db.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(size),0), MAX(verified_at) FROM backup_copies WHERE destination_id = ?`, d.ID).
			Scan(&d.Copies, &d.CopyBytes, &last)
		d.LastVerifiedAt = strPtr(last)
		for _, p := range plans {
			for _, id := range p.Destinations {
				if id == d.ID {
					d.UsedBy = append(d.UsedBy, p.Name)
					break
				}
			}
		}
	}
	return out, nil
}

// GetDestination returns one destination (with its sealed secret) or
// ErrNotFound.
func GetDestination(ctx context.Context, db *sql.DB, id string) (*Destination, error) {
	return scanDestination(db.QueryRowContext(ctx, `SELECT `+destinationColumns+` FROM backup_offsite_destinations WHERE id = ?`, id))
}

// NormalizeDestination validates a new S3 destination without touching the
// network (offsite.S3Config.Validate: https only unless private networks
// are allowed explicitly, the SSRF guard on the endpoint, bucket naming).
func NormalizeDestination(d *Destination, secret string) error {
	d.Name, d.Endpoint, d.Region = strings.TrimSpace(d.Name), strings.TrimSpace(d.Endpoint), strings.TrimSpace(d.Region)
	d.Bucket, d.Prefix, d.AccessKeyID = strings.TrimSpace(d.Bucket), strings.Trim(strings.TrimSpace(d.Prefix), "/"), strings.TrimSpace(d.AccessKeyID)
	if d.Kind == "" {
		d.Kind = offsite.KindS3
	}
	if d.Kind != offsite.KindS3 {
		return invalid("kind must be s3 (Google Drive comes later)")
	}
	if d.Name == "" {
		d.Name = d.Bucket
		if d.Prefix != "" {
			d.Name += "/" + d.Prefix
		}
	}
	if len(d.Name) > 120 {
		return invalid("name is at most 120 characters")
	}
	if err := d.S3Config(secret).Validate(); err != nil {
		return invalid("%v", err)
	}
	return nil
}

// InsertDestination stores a normalized destination; secretEnc is the vault
// envelope of its secret access key. d.ID is set.
func InsertDestination(ctx context.Context, ex execer, d *Destination, secretEnc, actor string, now time.Time) error {
	d.ID = newID("bdst_")
	d.CreatedAt = ts(now)
	if d.Kind == "" {
		d.Kind = offsite.KindS3
	}
	if actor != "" {
		d.CreatedBy = &actor
	}
	if d.UsedBy == nil {
		d.UsedBy = []string{}
	}
	d.secretEnc = secretEnc
	_, err := ex.ExecContext(ctx, `INSERT INTO backup_offsite_destinations (`+destinationColumns+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		d.ID, d.Name, d.Kind, d.Endpoint, d.Region, d.Bucket, d.Prefix, d.AccessKeyID, secretEnc, boolInt(d.PathStyle),
		boolInt(d.AllowPrivateNetwork), nullStrPtr(d.LastTestAt), nullStrPtr(d.LastTestError), nullStr(actor), d.CreatedAt)
	return err
}

// DeleteDestination removes a destination no plan uses, with its copy
// records (the remote objects stay; they are the admin's to delete).
func DeleteDestination(ctx context.Context, db *sql.DB, ex execer, id string) ([]string, error) {
	if _, err := GetDestination(ctx, db, id); err != nil {
		return nil, err
	}
	plans, err := ListPlans(ctx, db)
	if err != nil {
		return nil, err
	}
	var used []string
	for _, p := range plans {
		for _, d := range p.Destinations {
			if d == id {
				used = append(used, p.Name)
				break
			}
		}
	}
	if len(used) > 0 {
		return used, ErrDestinationInUse
	}
	if _, err := ex.ExecContext(ctx, `DELETE FROM backup_copies WHERE destination_id = ?`, id); err != nil {
		return nil, err
	}
	if _, err := ex.ExecContext(ctx, `DELETE FROM backup_offsite_destinations WHERE id = ?`, id); err != nil {
		return nil, err
	}
	return nil, nil
}

// RecordDestinationTest stores the outcome of a connection test.
func RecordDestinationTest(ctx context.Context, db *sql.DB, id string, at time.Time, testErr error) error {
	var msg any
	if testErr != nil {
		msg = testErr.Error()
	}
	_, err := db.ExecContext(ctx, `UPDATE backup_offsite_destinations SET last_test_at = ?, last_test_error = ? WHERE id = ?`, ts(at), msg, id)
	return err
}

// ValidatePlanDestinations checks that every destination a plan names is
// "local" or a stored destination, and puts "local" first: every bundle is
// written on this server before it is copied anywhere.
func ValidatePlanDestinations(ctx context.Context, db *sql.DB, p *Plan) error {
	out := []string{DestinationLocal}
	seen := map[string]bool{DestinationLocal: true}
	for _, id := range p.Destinations {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		if _, err := GetDestination(ctx, db, id); err != nil {
			if errors.Is(err, ErrNotFound) {
				return invalid("no backup destination %q (add it under Storage, or use local)", id)
			}
			return err
		}
		seen[id] = true
		out = append(out, id)
	}
	p.Destinations = out
	return nil
}

// DestinationOpener builds a transfer client for a stored destination.
type DestinationOpener func(ctx context.Context, id string) (offsite.Destination, *Destination, error)

// DBDestinations opens destinations from backup_offsite_destinations,
// unsealing the secret with the vault key.
func DBDestinations(db *sql.DB) DestinationOpener {
	return func(ctx context.Context, id string) (offsite.Destination, *Destination, error) {
		d, err := GetDestination(ctx, db, id)
		if err != nil {
			return nil, nil, err
		}
		secret, err := encryption.Decrypt(d.secretEnc)
		if err != nil {
			return nil, d, fmt.Errorf("cannot unseal the secret of destination %s: %w", d.Name, err)
		}
		s3, err := offsite.NewS3(d.S3Config(secret))
		if err != nil {
			return nil, d, err
		}
		return s3, d, nil
	}
}

// ─── Copies ─────────────────────────────────────────────────────────────────

// Copy is a verified off-site copy of a bundle.
type Copy struct {
	BundlePath    string `json:"bundle_path"`
	DestinationID string `json:"destination_id"`
	Key           string `json:"key"`
	Size          int64  `json:"size"`
	SHA256        string `json:"sha256"`
	VerifiedAt    string `json:"verified_at"`
}

// RecordCopy stores a verified copy (replacing an earlier one of the same
// bundle at the same destination).
func RecordCopy(ctx context.Context, ex execer, c Copy) error {
	_, err := ex.ExecContext(ctx, `INSERT INTO backup_copies (id, bundle_path, destination_id, object_key, size, sha256, verified_at)
		VALUES (?,?,?,?,?,?,?)
		ON CONFLICT(bundle_path, destination_id) DO UPDATE SET object_key=excluded.object_key, size=excluded.size,
		sha256=excluded.sha256, verified_at=excluded.verified_at`,
		newID("bcp_"), c.BundlePath, c.DestinationID, c.Key, c.Size, c.SHA256, c.VerifiedAt)
	return err
}

// CopiesOf lists a bundle's verified off-site copies.
func CopiesOf(ctx context.Context, db *sql.DB, bundlePath string) ([]Copy, error) {
	rows, err := db.QueryContext(ctx, `SELECT bundle_path, destination_id, object_key, size, sha256, verified_at FROM backup_copies
		WHERE bundle_path = ? ORDER BY destination_id`, bundlePath)
	if err != nil {
		if strings.Contains(err.Error(), "no such table") {
			return nil, nil
		}
		return nil, err
	}
	defer rows.Close()
	var out []Copy
	for rows.Next() {
		var c Copy
		if err := rows.Scan(&c.BundlePath, &c.DestinationID, &c.Key, &c.Size, &c.SHA256, &c.VerifiedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// copiedPaths is every bundle path with at least one verified copy.
func copiedPaths(ctx context.Context, db *sql.DB) map[string]bool {
	out := map[string]bool{}
	rows, err := db.QueryContext(ctx, `SELECT DISTINCT bundle_path FROM backup_copies`)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var p string
		if rows.Scan(&p) == nil {
			out[p] = true
		}
	}
	return out
}

// ObjectKey is where a bundle lands at a destination (under its prefix):
// instance/<file> or workspaces/<workspace id>/<file>.
func ObjectKey(scope, workspaceID, bundlePath string) string {
	name := filepath.Base(bundlePath)
	if scope == ScopeInstance || workspaceID == "" {
		return "instance/" + name
	}
	return "workspaces/" + workspaceID + "/" + name
}
