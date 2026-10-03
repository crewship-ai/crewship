package backup

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// These regressions run in the ordinary Linux CI suite, without a Docker
// daemon. The shell adapter uses a temporary crew root and the test process's
// UID/GID; cross-identity permissions remain covered by the livedocker test.
type acceptanceRestoreOps struct {
	restoreRecOps
	execAs   func(context.Context, string, []string) (int, []byte, error)
	copyPath func(ExtractSpec, io.Reader) error
}

func (o *acceptanceRestoreOps) ExecAs(ctx context.Context, _ string, user string, args []string) (int, []byte, error) {
	return o.execAs(ctx, user, args)
}
func (o *acceptanceRestoreOps) CopyToPath(_ context.Context, _ string, spec ExtractSpec, r io.Reader) error {
	return o.copyPath(spec, r)
}

func acceptanceRestorePayload(t *testing.T, entries []payloadEntry) *ExtractedPayload {
	t.Helper()
	p, err := ExtractPayload(context.Background(), bytes.NewReader(buildPayloadTarZst(t, entries)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := p.Close(); err != nil {
			t.Error(err)
		}
	})
	return p
}

func TestRestoreAcceptanceOrdinaryFilesDoNotProbeEmptyMemory(t *testing.T) {
	p := acceptanceRestorePayload(t, []payloadEntry{{name: "crew/alpha/shared/ordinary.txt", body: []byte("ordinary")}})
	copied := false
	ops := &acceptanceRestoreOps{
		execAs: func(_ context.Context, user string, _ []string) (int, []byte, error) {
			// /crew belongs to the agent. An empty memory section must not probe
			// its root as the sidecar or run the memory permission pass.
			if user != agentUser {
				return 1, []byte("permission denied"), nil
			}
			return 0, nil, nil
		},
		copyPath: func(spec ExtractSpec, r io.Reader) error {
			if spec.User != agentUser || spec.Dest != ContainerCrewPath {
				return fmt.Errorf("unexpected extraction: %+v", spec)
			}
			entries := readPlainTar(t, r)
			if len(entries) != 1 || entries["shared/ordinary.txt"] != "ordinary" {
				return fmt.Errorf("wrong restored files: %v", entries)
			}
			copied = true
			return nil
		},
	}
	if err := RestoreCrew(context.Background(), ops, "owned-fixture", "alpha", p); err != nil {
		t.Fatal(err)
	}
	if !copied {
		t.Fatal("ordinary file was not restored")
	}
}

func TestRestoreAcceptanceMissingMemoryDirectories(t *testing.T) {
	for _, symlink := range []bool{false, true} {
		t.Run(fmt.Sprintf("symlink=%t", symlink), func(t *testing.T) {
			root := t.TempDir()
			if err := os.Mkdir(filepath.Join(root, "shared"), 0755); err != nil {
				t.Fatal(err)
			}
			outside := t.TempDir()
			if symlink {
				if err := os.Symlink(outside, filepath.Join(root, "shared", ".memory")); err != nil {
					t.Fatal(err)
				}
			}
			p := acceptanceRestorePayload(t, []payloadEntry{{name: "crew/alpha/shared/.memory/nested/fact.txt", body: []byte("remember")}})
			// Execute the production commands, replacing only the container root
			// and numeric identities. No global path or privilege changes are made.
			replace := strings.NewReplacer("/crew", root, "1001", strconv.Itoa(os.Getuid()), "1002", strconv.Itoa(os.Getgid()))
			copies := 0
			ops := &acceptanceRestoreOps{
				execAs: func(ctx context.Context, _ string, args []string) (int, []byte, error) {
					mapped := append([]string(nil), args...)
					for i := range mapped {
						mapped[i] = replace.Replace(mapped[i])
					}
					out, err := exec.CommandContext(ctx, mapped[0], mapped[1:]...).CombinedOutput()
					var exit *exec.ExitError
					if errors.As(err, &exit) {
						return exit.ExitCode(), out, nil
					}
					return 0, out, err
				},
				copyPath: func(spec ExtractSpec, r io.Reader) error {
					copies++
					if spec.User != memoryWriterUser || !spec.UnlinkFirst || !spec.PreserveModes || !spec.PreserveTimes {
						return fmt.Errorf("memory extraction loses ownership/metadata policy: %+v", spec)
					}
					tr := tar.NewReader(r)
					h, err := tr.Next()
					if err != nil {
						return err
					}
					if h.Name != "shared/.memory/nested/fact.txt" || h.Typeflag != tar.TypeReg || h.Mode != 0644 || h.ModTime.Unix() != 0 {
						return fmt.Errorf("unexpected memory entry: %+v", h)
					}
					// The sidecar extraction needs the shared skeleton BEFORE it writes.
					for _, rel := range []string{"shared/.memory", "shared/.memory/nested"} {
						st, err := os.Stat(filepath.Join(root, rel))
						if err != nil {
							return err
						}
						if !st.IsDir() || st.Mode().Perm() != 0775 || st.Mode()&os.ModeSetgid == 0 {
							return fmt.Errorf("unprepared memory directory %s: %v", rel, st.Mode())
						}
					}
					body, err := io.ReadAll(tr)
					if err != nil {
						return err
					}
					if string(body) != "remember" {
						return fmt.Errorf("wrong content: %q", body)
					}
					if _, err := tr.Next(); err != io.EOF {
						return fmt.Errorf("unexpected extra memory entry: %v", err)
					}
					return os.WriteFile(filepath.Join(root, h.Name), body, 0644)
				},
			}
			err := RestoreCrew(context.Background(), ops, "owned-fixture", "alpha", p)
			if symlink {
				if !errors.Is(err, ErrRestorePreflight) || copies != 0 {
					t.Fatalf("symlink must refuse before extraction: copies=%d err=%v", copies, err)
				}
				entries, readErr := os.ReadDir(outside)
				if readErr != nil || len(entries) != 0 {
					t.Fatalf("restore touched symlink target: %v, %v", entries, readErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if copies != 1 {
				t.Fatalf("got %d extractions, want only memory", copies)
			}
		})
	}
}

func TestRestoreAcceptanceUnlinkFirstAllowsReadonlyFile(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root bypasses the DAC permission boundary exercised here")
	}
	root := t.TempDir()
	target := filepath.Join(root, "fact.txt")
	if err := os.WriteFile(target, []byte("old"), 0444); err != nil {
		t.Fatal(err)
	}
	ops := &acceptanceRestoreOps{execAs: func(ctx context.Context, _ string, args []string) (int, []byte, error) {
		out, err := exec.CommandContext(ctx, args[0], args[1:]...).CombinedOutput()
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return exit.ExitCode(), out, nil
		}
		return 0, out, err
	}}
	spec := ExtractSpec{Dest: root, User: strconv.Itoa(os.Getuid()), UnlinkFirst: true}
	if err := probeWritable(context.Background(), ops, "owned-fixture", "alpha", spec, map[string]bool{".": true}, map[string]bool{"fact.txt": true}); err != nil {
		t.Fatalf("a readonly file can be unlinked through its writable parent: %v", err)
	}
	// The same file must still block an extraction that opens it in place.
	spec.UnlinkFirst = false
	if err := probeWritable(context.Background(), ops, "owned-fixture", "alpha", spec, map[string]bool{".": true}, map[string]bool{"fact.txt": true}); err == nil {
		t.Fatal("in-place overwrite of a readonly file must be refused")
	}
}
