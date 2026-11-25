//go:build linux

package backup

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/writerlease"
	"golang.org/x/sys/unix"
)

func writeForceFixture(t *testing.T, path, value string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(value), 0640); err != nil {
		t.Fatal(err)
	}
}

func assertForceFixture(t *testing.T, path, value string) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil || string(b) != value {
		t.Fatalf("read %s: %q, %v; want %q", path, b, err, value)
	}
}

func TestForceStageFilesystemBoundaries(t *testing.T) {
	for _, kind := range []string{"symlink", "hardlink", "fifo", "target symlink"} {
		t.Run(kind, func(t *testing.T) {
			parent := t.TempDir()
			target := filepath.Join(parent, "data")
			if err := os.Mkdir(target, 0700); err != nil {
				t.Fatal(err)
			}
			external := filepath.Join(parent, "operator")
			writeForceFixture(t, external, "safe")
			inside := filepath.Join(target, "alias")
			var err error
			switch kind {
			case "symlink":
				err = os.Symlink(external, inside)
			case "hardlink":
				err = os.Link(external, inside)
			case "fifo":
				err = unix.Mkfifo(inside, 0600)
			case "target symlink":
				link := filepath.Join(parent, "link")
				err = os.Symlink(target, link)
				target = link
			}
			if err != nil {
				t.Fatal(err)
			}
			if s, err := beginForceStage(t.Context(), target); err == nil {
				s.close()
				t.Fatal("accepted", kind)
			}
			assertForceFixture(t, external, "safe")
			names, err := filepath.Glob(filepath.Join(parent, ".crewship-recovery-*"))
			if err != nil || len(names) != 0 {
				t.Fatalf("scratch left: %v %v", names, err)
			}
		})
	}
	rootMount, err := forceMount("/")
	if err != nil {
		t.Fatal(err)
	}
	if f, _, err := openForceNode("/proc", rootMount); err == nil {
		f.Close()
		t.Fatal("crossed proc mount")
	}
	if l, err := writerlease.GenerationLock("/proc", true); !errors.Is(err, writerlease.ErrUnsupportedStorage) {
		if l != nil {
			l.Close()
		}
		t.Fatalf("procfs lock: %v", err)
	}
}

func TestForceStageWriterFences(t *testing.T) {
	parent := t.TempDir()
	target := filepath.Join(parent, "instance")
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(target, "crewship.db")
	l, err := writerlease.Acquire(path)
	if err != nil {
		t.Fatal(err)
	}
	if s, e := beginForceStage(t.Context(), target); !errors.Is(e, writerlease.ErrHeld) {
		if s != nil {
			s.close()
		}
		t.Fatalf("live database: %v", e)
	}
	l.Close()
	s, err := beginForceStage(t.Context(), target)
	if err != nil {
		t.Fatal(err)
	}
	defer s.close()
	if other, e := beginForceStage(t.Context(), target); !errors.Is(e, writerlease.ErrHeld) {
		if other != nil {
			other.close()
		}
		t.Fatalf("parallel recovery: %v", e)
	}
	for _, step := range []string{"before", "after"} {
		if step == "after" {
			if done, e := s.commit(t.Context()); !done || e != nil {
				t.Fatal(done, e)
			}
		}
		if lease, e := writerlease.Acquire(path); !errors.Is(e, writerlease.ErrHeld) {
			if lease != nil {
				lease.Close()
			}
			t.Fatalf("%s publication writer: %v", step, e)
		}
	}
}

func TestForceStageCommitFailures(t *testing.T) {
	for _, kind := range []string{"canceled", "ENOSPC"} {
		t.Run(kind, func(t *testing.T) {
			target := t.TempDir()
			path := filepath.Join(target, "original")
			writeForceFixture(t, path, "original")
			before, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			s, err := beginForceStage(t.Context(), target)
			if err != nil {
				t.Fatal(err)
			}
			defer s.close()
			writeForceFixture(t, filepath.Join(s.dir, "original"), "changed")
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			want := error(unix.ENOSPC)
			if kind == "canceled" {
				cancel()
				want = context.Canceled
			}
			done, err := s.publish(ctx, func(int, string, int, string, uint) error { return unix.ENOSPC })
			if done || !errors.Is(err, want) {
				t.Fatal(done, err)
			}
			after, err := os.Stat(path)
			if err != nil || !os.SameFile(before, after) {
				t.Fatal("lost original inode", err)
			}
			assertForceFixture(t, path, "original")
		})
	}
}

func TestForceStageRetainedMetadata(t *testing.T) {
	target := t.TempDir()
	path := filepath.Join(target, "operator")
	writeForceFixture(t, path, "keep")
	stamp := time.Unix(1686400000, 123456789)
	if err := os.Chtimes(path, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	xerr := unix.Setxattr(path, "user.test", []byte("metadata"), 0)
	if xerr != nil && !errors.Is(xerr, unix.ENOTSUP) {
		t.Fatal(xerr)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	s, err := beginForceStage(t.Context(), target)
	if err != nil {
		t.Fatal(err)
	}
	defer s.close()
	if done, err := s.commit(t.Context()); !done || err != nil {
		t.Fatal(done, err)
	}
	old, err := os.Stat(filepath.Join(s.previous, "operator"))
	if err != nil || !os.SameFile(before, old) {
		t.Fatal("original generation not retained", err)
	}
	now, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	a, b := before.Sys().(*syscall.Stat_t), now.Sys().(*syscall.Stat_t)
	if os.SameFile(old, now) || now.Mode() != before.Mode() || !now.ModTime().Equal(stamp) || a.Uid != b.Uid || a.Gid != b.Gid {
		t.Fatal("operator file metadata changed")
	}
	if xerr == nil {
		attrs, err := forceAttrs(path)
		if err != nil || string(attrs["user.test"]) != "metadata" {
			t.Fatal("lost extended attributes", err)
		}
	}
}

func TestForceStageDeathAtExchange(t *testing.T) {
	if target := os.Getenv("CREWSHIP_TEST_FORCE_STAGE_TARGET"); target != "" {
		s, err := beginForceStage(t.Context(), target)
		if err != nil {
			t.Fatal(err)
		}
		writeForceFixture(t, filepath.Join(s.dir, "marker"), "new")
		_, err = s.publish(t.Context(), func(a int, b string, c int, d string, flags uint) error {
			if os.Getenv("CREWSHIP_TEST_FORCE_STAGE_EDGE") == "after" {
				if err := unix.Renameat2(a, b, c, d, flags); err != nil {
					return err
				}
			}
			return unix.Kill(os.Getpid(), unix.SIGKILL)
		})
		t.Fatal("child survived publication hook", err)
	}
	for _, edge := range []string{"before", "after"} {
		t.Run(edge, func(t *testing.T) {
			target := t.TempDir()
			writeForceFixture(t, filepath.Join(target, "marker"), "old")
			cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestForceStageDeathAtExchange$")
			cmd.Env = append(os.Environ(), "CREWSHIP_TEST_FORCE_STAGE_EDGE="+edge, "CREWSHIP_TEST_FORCE_STAGE_TARGET="+target)
			out, err := cmd.CombinedOutput()
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.Sys().(syscall.WaitStatus).Signal() != unix.SIGKILL {
				t.Fatalf("child: %s, %v", out, err)
			}
			want, other := "old", "new"
			if edge == "after" {
				want, other = other, want
			}
			assertForceFixture(t, filepath.Join(target, "marker"), want)
			kept, err := filepath.Glob(filepath.Join(filepath.Dir(target), ".crewship-recovery-*", "generation", "marker"))
			if err != nil || len(kept) != 1 {
				t.Fatalf("sibling generation: %v %v", kept, err)
			}
			assertForceFixture(t, kept[0], other)
			s, err := beginForceStage(t.Context(), target)
			if err != nil {
				t.Fatal("kernel leases not released", err)
			}
			s.close()
		})
	}
}

func TestForceStageAbsentTarget(t *testing.T) {
	parent := t.TempDir()
	target := filepath.Join(parent, "new")
	s, err := beginForceStage(t.Context(), target)
	if err != nil {
		t.Fatal(err)
	}
	defer s.close()
	if s.previous != "" {
		t.Fatal("absent target has a previous generation")
	}
	if _, err = os.Stat(target); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("target visible before commit: %v", err)
	}
	writeForceFixture(t, filepath.Join(s.dir, "marker"), "new")
	if err = os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	if done, e := s.commit(t.Context()); done || !errors.Is(e, unix.EEXIST) {
		t.Fatalf("overwrote concurrently created target: %v %v", done, e)
	}
	if err = os.Remove(target); err != nil {
		t.Fatal(err)
	}
	if done, e := s.commit(t.Context()); !done || e != nil {
		t.Fatal(done, e)
	}
	assertForceFixture(t, filepath.Join(target, "marker"), "new")
}

func TestForceStageAbortsPartialCopy(t *testing.T) {
	for _, failure := range []string{"full", "cancel"} {
		t.Run(failure, func(t *testing.T) {
			target := t.TempDir()
			writeForceFixture(t, filepath.Join(target, "original"), "old")
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var scratch string
			s, err := prepareForceStage(ctx, target, func(ctx context.Context, _, dst string, _ uint64) error {
				scratch = filepath.Dir(dst)
				if err := os.Mkdir(dst, 0700); err != nil {
					return err
				}
				writeForceFixture(t, filepath.Join(dst, "partial"), "half a file")
				if failure == "cancel" {
					cancel()
					return ctx.Err()
				}
				full, err := os.OpenFile("/dev/full", os.O_WRONLY, 0)
				if err != nil {
					return err
				}
				defer full.Close()
				_, err = full.Write([]byte("more data"))
				return err
			})
			want := error(unix.ENOSPC)
			if failure == "cancel" {
				want = context.Canceled
			}
			if s != nil || !errors.Is(err, want) {
				t.Fatalf("partial copy: %v %v", s, err)
			}
			if _, err = os.Stat(scratch); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("staging cleanup: %v", err)
			}
			assertForceFixture(t, filepath.Join(target, "original"), "old")
			retry, err := beginForceStage(t.Context(), target)
			if err != nil {
				t.Fatal("lock survived failed copy", err)
			}
			retry.close()
		})
	}
}
