package backup

import (
	"archive/tar"
	"fmt"
	"io"
	"time"

	"github.com/klauspost/compress/zstd"
)

// TarZstWriter streams files into a tar archive that is compressed
// on-the-fly with zstd. Close MUST be called to flush trailers on both
// the tar and zstd layers; the returned archive is otherwise truncated.
//
// The writer owns the lifecycle of the internal zstd encoder but NOT
// the outer io.Writer it was constructed with — closing the underlying
// sink is the caller's responsibility.
type TarZstWriter struct {
	tw   *tar.Writer
	zw   *zstd.Encoder
	sink io.Writer
}

// NewTarZstWriter returns a TarZstWriter that writes compressed bytes
// into sink. Standard compression level is used; tuning is deferred to
// V2 when we benchmark against real workload sizes.
func NewTarZstWriter(sink io.Writer) (*TarZstWriter, error) {
	zw, err := zstd.NewWriter(sink)
	if err != nil {
		return nil, fmt.Errorf("backup: init zstd writer: %w", err)
	}
	return &TarZstWriter{
		tw:   tar.NewWriter(zw),
		zw:   zw,
		sink: sink,
	}, nil
}

// WriteFile writes a single file entry with the given name, mode, and
// modification time. The entire content is read from src. For large
// inputs (Docker volumes, workspace bind mounts) the caller should
// prefer WriteStream, which avoids buffering in memory.
func (w *TarZstWriter) WriteFile(name string, mode int64, modTime time.Time, src []byte) error {
	hdr := &tar.Header{
		Name:     name,
		Mode:     mode,
		Size:     int64(len(src)),
		ModTime:  modTime,
		Typeflag: tar.TypeReg,
	}
	if err := w.tw.WriteHeader(hdr); err != nil {
		return fmt.Errorf("backup: tar header for %q: %w", name, err)
	}
	if _, err := w.tw.Write(src); err != nil {
		return fmt.Errorf("backup: tar write body for %q: %w", name, err)
	}
	return nil
}

// WriteStream writes an entry whose size is known ahead of time. The
// caller supplies size so we can set the tar header correctly before
// streaming. src must deliver exactly size bytes.
//
// Ownership defaults to 0:0. Callers repacking real container content
// must use WriteStreamOwned instead — see #1714.
func (w *TarZstWriter) WriteStream(name string, mode int64, modTime time.Time, size int64, src io.Reader) error {
	return w.WriteStreamOwned(name, mode, modTime, size, 0, 0, "", "", src)
}

// WriteStreamOwned is WriteStream with the source entry's ownership
// carried onto the header.
//
// Why it has to exist: WriteStream left Uid/Gid at their zero values, so
// every regular file in every bundle recorded uid 0 gid 0 no matter who
// owned it in the container — while RepackTarWithExcludes was, in the
// same loop, correctly copying Uid/Gid onto directory and symlink
// headers. The bundle therefore described a tree whose directories
// belonged to the agent and whose files belonged to root, which is not a
// state any container was ever in. Restores that trust the header
// reproduced that impossible tree faithfully (#1714).
//
// Uname/Gname ride along because a cross-instance restore may want to
// resolve by name when the numeric id means nothing on the target.
func (w *TarZstWriter) WriteStreamOwned(name string, mode int64, modTime time.Time, size int64, uid, gid int, uname, gname string, src io.Reader) error {
	hdr := &tar.Header{
		Name:     name,
		Mode:     mode,
		Size:     size,
		ModTime:  modTime,
		Typeflag: tar.TypeReg,
		Uid:      uid,
		Gid:      gid,
		Uname:    uname,
		Gname:    gname,
	}
	if err := w.tw.WriteHeader(hdr); err != nil {
		return fmt.Errorf("backup: tar header for %q: %w", name, err)
	}
	if _, err := io.CopyN(w.tw, src, size); err != nil {
		return fmt.Errorf("backup: tar stream body for %q: %w", name, err)
	}
	return nil
}

// Close flushes tar trailers and closes the zstd encoder, producing a
// well-formed archive in sink. The underlying sink is NOT closed.
func (w *TarZstWriter) Close() error {
	if err := w.tw.Close(); err != nil {
		_ = w.zw.Close()
		return fmt.Errorf("backup: close tar: %w", err)
	}
	if err := w.zw.Close(); err != nil {
		return fmt.Errorf("backup: close zstd: %w", err)
	}
	return nil
}

// TarZstReader is the read-side counterpart. It decompresses the zstd
// stream on the fly and exposes a *tar.Reader to walk the entries.
// Close releases resources held by the zstd decoder.
type TarZstReader struct {
	tr *tar.Reader
	zr *zstd.Decoder
}

// NewTarZstReader returns a TarZstReader over src.
func NewTarZstReader(src io.Reader) (*TarZstReader, error) {
	zr, err := zstd.NewReader(src)
	if err != nil {
		return nil, fmt.Errorf("backup: init zstd reader: %w", err)
	}
	return &TarZstReader{
		tr: tar.NewReader(zr),
		zr: zr,
	}, nil
}

// Next advances to the next entry header in the archive. It returns
// io.EOF when the archive is exhausted.
func (r *TarZstReader) Next() (*tar.Header, error) {
	return r.tr.Next()
}

// Read reads the body of the current entry.
func (r *TarZstReader) Read(p []byte) (int, error) {
	return r.tr.Read(p)
}

// Close releases the zstd decoder. Safe to call multiple times.
func (r *TarZstReader) Close() error {
	r.zr.Close()
	return nil
}
