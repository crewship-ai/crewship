package backup

import (
	"archive/tar"
	"context"
	"io"
	"path"
	"strings"
)

// isMemoryEntry reports whether a tar path inside the crew section
// belongs to a `.memory` tree.
//
// The crew archive spans two ownership domains: an agent's own state
// (`.claude.json`, `.mcp.json`, `.claude/`) is 1001 at mode 0600, and
// `.memory` is group 1002 with its files written by the sidecar as
// 1002. No single extraction identity can write both, which is why the
// section is split rather than given a cleverer identity (#1746).
func isMemoryEntry(name string) bool {
	for _, seg := range strings.Split(path.Clean(name), "/") {
		if seg == ".memory" {
			return true
		}
	}
	return false
}

// filterTar re-streams a tar keeping only the entries keep() accepts.
// The reader is consumed lazily; the caller still owns the original.
func filterTar(src io.ReadCloser, keep func(*tar.Header) bool, contexts ...context.Context) io.ReadCloser {
	ctx := context.Background()
	if len(contexts) > 0 {
		ctx = contexts[0]
	}
	pr, pw := io.Pipe()
	go func() {
		defer src.Close()
		tr := newRestoreTarReader(ctx, src)
		defer tr.Close()
		tw := tar.NewWriter(RestoreWriter(ctx, pw))
		for {
			hdr, err := tr.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				_ = tw.Close()
				_ = pw.CloseWithError(err)
				return
			}
			if !keep(hdr) {
				continue
			}
			if err := tw.WriteHeader(hdr); err != nil {
				_ = pw.CloseWithError(err)
				return
			}
			if _, err := io.Copy(tw, tr); err != nil {
				_ = pw.CloseWithError(err)
				return
			}
		}
		if err := tw.Close(); err != nil {
			_ = pw.CloseWithError(err)
			return
		}
		_ = pw.Close()
	}()
	return pr
}

// memoryFilesTar keeps the `.memory` files and nothing else: no
// directory metadata, which the sidecar cannot apply to agent-owned
// directories. RestoreCrew prepares the shared directory skeleton before
// extraction. Nothing outside `.memory` belongs to this ownership domain.
func memoryFilesTar(src io.ReadCloser, contexts ...context.Context) io.ReadCloser {
	return filterTar(src, func(h *tar.Header) bool {
		return h.Typeflag != tar.TypeDir && isMemoryEntry(h.Name)
	}, contexts...)
}

// agentStateTar keeps everything that is NOT memory — the agent's own
// files, restored under the agent as they always were.
func agentStateTar(src io.ReadCloser, contexts ...context.Context) io.ReadCloser {
	return filterTar(src, func(h *tar.Header) bool { return !isMemoryEntry(h.Name) }, contexts...)
}

// filesOnlyTar re-streams a tar with every directory entry removed.
//
// It exists for the crew memory section (#1746). That section's archive
// carries the directories above `.memory` — `agents/`, `agents/<slug>/`
// — which are owned by the agent, while the files inside `.memory` are
// owned by the sidecar. No single extraction identity can apply modes
// or mtimes to both: chmod and utime are owner rights, so tar exits 2
// partway through, having already written some of the section.
//
// Dropping the directory entries removes the conflict rather than
// working around it. Every `.memory` directory is created by
// prepMemoryDirs when the agent starts, and a restore already requires
// a running container, so there is nothing for these entries to
// create. Any parent a file still needs, tar makes implicitly.
//
// The reader is consumed lazily; the caller still owns the original.
func filesOnlyTar(src io.ReadCloser, contexts ...context.Context) io.ReadCloser {
	ctx := context.Background()
	if len(contexts) > 0 {
		ctx = contexts[0]
	}
	pr, pw := io.Pipe()
	go func() {
		defer src.Close()
		tr := newRestoreTarReader(ctx, src)
		defer tr.Close()
		tw := tar.NewWriter(RestoreWriter(ctx, pw))
		for {
			hdr, err := tr.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				_ = tw.Close()
				_ = pw.CloseWithError(err)
				return
			}
			if hdr.Typeflag == tar.TypeDir {
				continue
			}
			if err := tw.WriteHeader(hdr); err != nil {
				_ = pw.CloseWithError(err)
				return
			}
			if _, err := io.Copy(tw, tr); err != nil {
				_ = pw.CloseWithError(err)
				return
			}
		}
		if err := tw.Close(); err != nil {
			_ = pw.CloseWithError(err)
			return
		}
		_ = pw.Close()
	}()
	return pr
}
