package backup

import (
	"archive/tar"
	"io"
)

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
func filesOnlyTar(src io.ReadCloser) io.ReadCloser {
	pr, pw := io.Pipe()
	go func() {
		defer src.Close()
		tr := tar.NewReader(src)
		tw := tar.NewWriter(pw)
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
