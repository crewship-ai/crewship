package database

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"sync"

	"modernc.org/sqlite"
)

// MemorySnapshot is a consistent copy of the database held in this
// process's memory only — never on disk. The whole-instance backup takes it
// inside the quiet window (SQLite's online backup API into an in-memory
// database, the same page copy SnapshotTo makes into a file), reads it
// through DB after the window, and serializes it straight into the sealed
// payload, so no unencrypted copy of the database ever touches a disk.
//
// It costs memory: about the database's size while it is held, and twice
// that for the moment Serialize copies it out.
type MemorySnapshot struct {
	// DB reads the snapshot. It has exactly one connection.
	DB *sql.DB
}

// SnapshotToMemory copies the database into memory with the online backup
// API (one Step(-1): a point-in-time image or an error, never a spin).
func SnapshotToMemory(ctx context.Context, db *sql.DB) (*MemorySnapshot, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire connection for snapshot: %w", err)
	}
	defer conn.Close()
	var dst driver.Conn
	err = conn.Raw(func(driverConn any) error {
		backupper, ok := driverConn.(sqliteOnlineBackup)
		if !ok {
			return fmt.Errorf("sqlite driver connection %T has no NewBackup method "+
				"(modernc.org/sqlite v1.56.0+ required for the online backup API)", driverConn)
		}
		bk, err := backupper.NewBackup(":memory:")
		if err != nil {
			return fmt.Errorf("open in-memory snapshot: %w", err)
		}
		more, stepErr := bk.Step(-1)
		remaining, pageCount := bk.Remaining(), bk.PageCount()
		if stepErr != nil || more {
			_ = bk.Finish() // releases the handle and closes the destination
			if stepErr != nil {
				return fmt.Errorf("copy pages into snapshot: %w", stepErr)
			}
			return fmt.Errorf("snapshot incomplete: %d of %d pages left after a full step", remaining, pageCount)
		}
		// Commit releases the backup handle and keeps the destination
		// connection open: it IS the snapshot.
		c, err := bk.Commit()
		if err != nil {
			return fmt.Errorf("finalize snapshot: %w", err)
		}
		dst = c
		return nil
	})
	if err != nil {
		return nil, err
	}
	sdb := sql.OpenDB(&singleConnector{conn: dst})
	sdb.SetMaxOpenConns(1)
	sdb.SetMaxIdleConns(1)
	sdb.SetConnMaxLifetime(0)
	sdb.SetConnMaxIdleTime(0)
	return &MemorySnapshot{DB: sdb}, nil
}

// Serialize returns the snapshot as a database file image.
func (m *MemorySnapshot) Serialize(ctx context.Context) ([]byte, error) {
	conn, err := m.DB.Conn(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	var out []byte
	err = conn.Raw(func(driverConn any) error {
		s, ok := driverConn.(interface{ Serialize() ([]byte, error) })
		if !ok {
			return fmt.Errorf("sqlite driver connection %T cannot serialize", driverConn)
		}
		b, err := s.Serialize()
		out = b
		return err
	})
	return out, err
}

// Close frees the snapshot.
func (m *MemorySnapshot) Close() error { return m.DB.Close() }

// singleConnector hands database/sql the snapshot's one connection. It
// cannot make another: an in-memory database lives in its connection.
type singleConnector struct {
	mu   sync.Mutex
	conn driver.Conn
}

var errSnapshotConnGone = errors.New("the in-memory snapshot's connection was closed")

func (c *singleConnector) Connect(context.Context) (driver.Conn, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn == nil {
		return nil, errSnapshotConnGone
	}
	conn := c.conn
	c.conn = nil
	return conn, nil
}

func (c *singleConnector) Driver() driver.Driver { return &sqlite.Driver{} }
