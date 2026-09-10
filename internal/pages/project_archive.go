package pages

import (
	"bufio"
	"bytes"
	"compress/zlib"
	"context"
	"crypto/rand"
	"crypto/sha1" // Git's object format, not the SHA-256 source integrity boundary.
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// VisitGitObjects streams only objects reachable from retained SQL checkpoints.
// No Git config, hooks, worktrees, remotes or archived destination paths cross
// the backup boundary. One raw object is bounded by the source envelope.
func (s *ProjectStore) VisitGitObjects(ctx context.Context, ws, page string, commits []string, visit func(string, string, []byte) error) error {
	if len(commits) == 0 {
		return nil
	}
	for _, id := range commits {
		if !ValidProjectCommit(id) {
			return errors.New("invalid checkpoint")
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	repo, err := s.gitPath(ctx, ws, page, false)
	if err != nil {
		return err
	}
	listing, err := runProjectGit(ctx, repo, []byte(strings.Join(commits, "\n")+"\n"), "rev-list", "--objects", "--no-object-names", "--stdin")
	if err != nil {
		return err
	}
	ids := strings.Fields(string(listing))
	if len(ids) > 65536 {
		return errors.New("too many Git objects")
	}
	for _, id := range ids {
		if !ValidProjectCommit(id) {
			return errors.New("invalid Git object list")
		}
	}
	cmd := projectGitCommand(ctx, repo, "cat-file", "--batch")
	cmd.Stdin = strings.NewReader(strings.Join(ids, "\n") + "\n")
	diagnostic := &gitOutput{limit: 64 << 10}
	cmd.Stderr = diagnostic
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err = cmd.Start(); err != nil {
		return err
	}
	defer func() { cancel(); _ = cmd.Wait() }()
	reader := bufio.NewReader(pipe)
	total := int64(0)
	for _, id := range ids {
		line, err := reader.ReadString('\n')
		if err != nil {
			return err
		}
		fields := strings.Fields(line)
		if len(fields) != 3 || fields[0] != id {
			return errors.New("invalid Git object header")
		}
		size, err := strconv.ParseInt(fields[2], 10, 64)
		if err != nil || size < 0 || size > MaxTransferBytes {
			return errors.New("Git object exceeds source limit")
		}
		total += size
		if total > 512<<20 {
			return errors.New("Git archive exceeds 512 MiB")
		}
		data := make([]byte, int(size))
		if _, err = io.ReadFull(reader, data); err != nil {
			return err
		}
		newline, err := reader.ReadByte()
		if err != nil || newline != '\n' {
			return errors.New("invalid Git object framing")
		}
		if err := validateGitObject(id, fields[1], data); err != nil {
			return err
		}
		if err := visit(id, fields[1], data); err != nil {
			return err
		}
	}
	// Read EOF before Wait so a truncated/erroring Git process cannot look complete.
	if _, err := reader.ReadByte(); err != io.EOF {
		return errors.New("unexpected trailing Git object output")
	}
	return cmd.Wait()
}

func projectGitCommand(ctx context.Context, repo string, args ...string) *exec.Cmd {
	base := []string{"--no-pager", "--git-dir=" + repo, "-c", "core.hooksPath=/dev/null", "-c", "core.attributesFile=/dev/null", "-c", "core.logAllRefUpdates=false", "-c", "gc.auto=0", "-c", "core.fsync=loose-object,reference", "-c", "commit.gpgSign=false"}
	cmd := exec.CommandContext(ctx, "git", append(base, args...)...)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_SYSTEM=/dev/null", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0", "GIT_NO_REPLACE_OBJECTS=1", "GIT_AUTHOR_NAME=Crewship", "GIT_AUTHOR_EMAIL=pages@crewship.invalid", "GIT_COMMITTER_NAME=Crewship", "GIT_COMMITTER_EMAIL=pages@crewship.invalid"}
	cmd.WaitDelay = time.Second
	return cmd
}
func validateGitObject(id, kind string, data []byte) error {
	if !ValidProjectCommit(id) || (kind != "blob" && kind != "tree" && kind != "commit") || len(data) > MaxTransferBytes {
		return errors.New("invalid Git object")
	}
	hash := sha1.New()
	fmt.Fprintf(hash, "%s %d%c", kind, len(data), byte(0))
	hash.Write(data)
	if fmt.Sprintf("%x", hash.Sum(nil)) != id {
		return errors.New("Git object checksum mismatch")
	}
	return nil
}

// ImportGitObject writes a verified loose object. It never asks Git to unpack an
// untrusted compressed pack, and never restores an executable hook/config.
func (s *ProjectStore) ImportGitObject(ctx context.Context, ws, page, id, kind string, data []byte) error {
	if err := validateGitObject(id, kind, data); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	repo, err := s.gitPath(ctx, ws, page, true)
	if err != nil {
		return err
	}
	return writeProjectGitObject(repo, id, kind, data)
}

func writeProjectGitObject(repo, id, kind string, data []byte) error {
	root, err := os.OpenRoot(repo)
	if err != nil {
		return err
	}
	defer root.Close()
	if err := root.MkdirAll("objects/"+id[:2], 0700); err != nil {
		return err
	}
	var buffer bytes.Buffer
	zw := zlib.NewWriter(&buffer)
	fmt.Fprintf(zw, "%s %d%c", kind, len(data), byte(0))
	if _, err := zw.Write(data); err != nil {
		return err
	}
	if err := zw.Close(); err != nil {
		return err
	}
	tmp := "objects/" + id[:2] + "/.restore-" + rand.Text()
	f, err := root.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer root.Remove(tmp)
	_, err = f.Write(buffer.Bytes())
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err := root.Rename(tmp, "objects/"+id[:2]+"/"+id[2:]); err != nil {
		return err
	}
	dir, err := root.Open("objects/" + id[:2])
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
func (s *ProjectStore) PinCheckpoint(ctx context.Context, ws, page, commit string) error {
	if !ValidProjectCommit(commit) {
		return errors.New("invalid checkpoint")
	}
	repo, err := s.gitPath(ctx, ws, page, false)
	if err != nil {
		return err
	}
	_, err = runProjectGit(ctx, repo, nil, "update-ref", "refs/checkpoints/"+commit, commit)
	return err
}

// CheckGitQuota also rejects symlinks before restored objects become SQL-visible.
// Restore holds an exclusive lease; checkpoint writes reserve their own headroom.
func (s *ProjectStore) CheckGitQuota(ctx context.Context, ws string) error {
	root, err := s.root(ws, false)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer root.Close()
	gitRoot, err := root.OpenRoot("git")
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer gitRoot.Close()
	var used int64
	return fs.WalkDir(gitRoot.FS(), ".", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("symlink in Page Git storage")
		}
		if !entry.IsDir() {
			info, err := entry.Info()
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() {
				return errors.New("invalid Page Git storage file")
			}
			used += info.Size()
			if used > MaxProjectGitBytes {
				return ErrProjectGitFull
			}
		}
		return nil
	})
}

// CheckGitRestoreQuota reserves the staged Git files against the existing
// namespace before restore writes anything there. The caller holds an exclusive
// target lease. Existing content-addressed objects do not count twice.
func (s *ProjectStore) CheckGitRestoreQuota(ctx context.Context, ws string, staged *ProjectStore) error {
	target, err := s.root(ws, true)
	if err != nil {
		return err
	}
	defer target.Close()
	stage, err := staged.root(ws, false)
	if err != nil {
		return err
	}
	defer stage.Close()
	var used int64
	walk := func(root *os.Root, incremental bool) error {
		git, err := root.OpenRoot("git")
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		defer git.Close()
		return fs.WalkDir(git.FS(), ".", func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if entry.Type()&os.ModeSymlink != 0 {
				return errors.New("symlink in Page Git storage")
			}
			if entry.IsDir() {
				return nil
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() {
				return errors.New("invalid Page Git storage file")
			}
			delta := info.Size()
			if incremental {
				previous, err := target.Lstat("git/" + path)
				if err == nil {
					if !previous.Mode().IsRegular() {
						return errors.New("invalid Page Git restore destination")
					}
					delta -= previous.Size()
				} else if !errors.Is(err, os.ErrNotExist) {
					return err
				}
			}
			// Never fund new objects by assuming an existing file will shrink.
			if delta > 0 {
				used += delta
			}
			if used > MaxProjectGitBytes {
				return ErrProjectGitFull
			}
			return nil
		})
	}
	if err := walk(target, false); err != nil {
		return err
	}
	return walk(stage, true)
}
