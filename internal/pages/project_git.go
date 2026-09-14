package pages

// Git is an immutable source archive. SQLite owns the current pointer: refs are
// revision pins, not authority to overwrite a draft. No checkout, add, hooks,
// filters, remote, submodule or credential helper is involved.
import (
	"bytes"
	"context"
	"crypto/sha1" // Git object identity; source integrity uses SHA-256.
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const MaxProjectGitBytes = 128 << 20

var ErrProjectGitFull = errors.New("Page Git storage quota reached")

func ValidProjectCommit(id string) bool {
	return len(id) == 40 && strings.Trim(id, "0123456789abcdef") == ""
}

func (s *ProjectStore) gitPath(ctx context.Context, ws, page string, create bool) (string, error) {
	if page == "" {
		return "", errors.New("missing Page identity")
	}
	root, err := s.root(ws, create)
	if err != nil {
		return "", err
	}
	defer root.Close()
	name := fmt.Sprintf("git/%x", sha256.Sum256([]byte(page)))
	if create {
		if err := root.MkdirAll(name, 0700); err != nil {
			return "", err
		}
	}
	dir, err := root.OpenRoot(name)
	if err != nil {
		return "", err
	}
	defer dir.Close()
	// The namespace is private to Crewship. Refuse symlinked repositories rather
	// than letting the external Git process follow one outside os.Root.
	info, err := root.Lstat(name)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", errors.New("invalid Git repository directory")
	}
	p := filepath.Join(s.Directory, fmt.Sprintf("%x", sha256.Sum256([]byte(ws))), filepath.FromSlash(name))
	if create {
		if _, err := dir.Stat("HEAD"); errors.Is(err, os.ErrNotExist) {
			if _, err = runProjectGit(ctx, p, nil, "init", "--bare", "--template=", "--initial-branch=main", p); err != nil {
				return "", err
			}
		} else if err != nil {
			return "", err
		}
	}
	return p, nil
}

type gitOutput struct {
	bytes.Buffer
	limit int
}

func (b *gitOutput) Write(p []byte) (int, error) {
	if len(p) > b.limit-b.Len() {
		return 0, errors.New("Git output exceeds limit")
	}
	return b.Buffer.Write(p)
}
func runProjectGit(ctx context.Context, repo string, input []byte, args ...string) ([]byte, error) {
	cmd := projectGitCommand(ctx, repo, args...)
	cmd.Stdin = bytes.NewReader(input)
	out := &gitOutput{limit: MaxTransferBytes}
	diagnostic := &gitOutput{limit: 64 << 10}
	cmd.Stdout = out
	cmd.Stderr = diagnostic
	cmd.WaitDelay = time.Second
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("Page Git %s: %w: %s", args[0], err, diagnostic.String())
	}
	return out.Bytes(), nil
}

type projectGitTree struct {
	files map[string][]byte
	dirs  map[string]*projectGitTree
}

func newProjectGitTree() *projectGitTree {
	return &projectGitTree{files: map[string][]byte{}, dirs: map[string]*projectGitTree{}}
}
func (t *projectGitTree) add(name string, data []byte) {
	parts := strings.SplitN(name, "/", 2)
	if len(parts) == 1 {
		t.files[name] = data
		return
	}
	if t.dirs[parts[0]] == nil {
		t.dirs[parts[0]] = newProjectGitTree()
	}
	t.dirs[parts[0]].add(parts[1], data)
}
func (t *projectGitTree) write(ctx context.Context, objects projectGitObjects) (string, error) {
	type entry struct{ name, order, mode, id string }
	entries := make([]entry, 0, len(t.files)+len(t.dirs))
	for name, data := range t.files {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		id, err := objects.add("blob", data)
		if err != nil {
			return "", err
		}
		entries = append(entries, entry{name, name, "100644", id})
	}
	for name, child := range t.dirs {
		id, err := child.write(ctx, objects)
		if err != nil {
			return "", err
		}
		entries = append(entries, entry{name, name + "/", "40000", id})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].order < entries[j].order })
	var tree bytes.Buffer
	for _, e := range entries {
		fmt.Fprintf(&tree, "%s %s%c", e.mode, e.name, byte(0))
		id, err := hex.DecodeString(e.id)
		if err != nil {
			return "", err
		}
		tree.Write(id)
	}
	return objects.add("tree", tree.Bytes())
}

type projectGitObject struct {
	kind string
	data []byte
}
type projectGitObjects map[string]projectGitObject

func (objects projectGitObjects) add(kind string, data []byte) (string, error) {
	hash := sha1.New()
	fmt.Fprintf(hash, "%s %d%c", kind, len(data), byte(0))
	hash.Write(data)
	id := fmt.Sprintf("%x", hash.Sum(nil))
	objects[id] = projectGitObject{kind: kind, data: data}
	return id, nil
}

// Checkpoint writes immutable objects and a content-addressed pin BEFORE the
// caller starts its CAS transaction. Failed CAS may leave an unreachable candidate;
// it is bounded by the same quota and never becomes the current draft.
func (s *ProjectStore) Checkpoint(ctx context.Context, ws, page, parent, spec, actor string, revision int64, p *SourceProject) (string, error) {
	if s == nil {
		return "", errors.New("Page source storage is not configured")
	}
	if parent != "" && !ValidProjectCommit(parent) {
		return "", errors.New("invalid parent commit")
	}
	if len(spec) > MaxSpecBytes || len(actor) > 256 || revision < 1 {
		return "", errors.New("invalid checkpoint metadata")
	}
	source, err := p.MarshalYAMLSource()
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	release, err := s.GitLease(ctx, ws, false)
	if err != nil {
		return "", err
	}
	defer release()
	mu := s.workspaceMutex(ws)
	mu.Lock()
	defer mu.Unlock()
	root, err := s.root(ws, true)
	if err != nil {
		return "", err
	}
	if err := root.MkdirAll("git", 0700); err != nil {
		root.Close()
		return "", err
	}
	root.Close()
	workspaceGit := filepath.Join(s.Directory, fmt.Sprintf("%x", sha256.Sum256([]byte(ws))), "git")
	var used int64
	err = filepath.WalkDir(workspaceGit, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if d.Type()&os.ModeSymlink != 0 {
			return errors.New("symlink in Page Git storage")
		}
		if !d.IsDir() {
			info, err := d.Info()
			if err != nil {
				return err
			}
			used += info.Size()
		}
		if used > MaxProjectGitBytes {
			return ErrProjectGitFull
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	// Reserve the worst-case input plus loose-object/tree overhead before writing.
	overhead := int64(1 << 20)
	for _, file := range p.Files {
		overhead += int64((strings.Count(file.Path, "/")+2)*512 + len(file.Path)*2)
	}
	if used+int64(len(source)+len(spec)+MaxProjectSourceBytes)+overhead > MaxProjectGitBytes {
		return "", ErrProjectGitFull
	}
	repo, err := s.gitPath(ctx, ws, page, true)
	if err != nil {
		return "", err
	}
	tree := newProjectGitTree()
	tree.add("source.yaml", source)
	tree.add("page.json", []byte(spec))
	for _, f := range p.Files {
		data, err := f.Bytes()
		if err != nil {
			return "", err
		}
		tree.add("project/"+f.Path, data)
	}
	objects := projectGitObjects{}
	treeID, err := tree.write(ctx, objects)
	if err != nil {
		return "", err
	}
	if err := writeProjectGitPack(ctx, repo, objects); err != nil {
		return "", err
	}
	args := []string{"commit-tree", treeID, "--no-gpg-sign"}
	if parent != "" {
		args = append(args, "-p", parent)
	}
	out, err := runProjectGit(ctx, repo, []byte(fmt.Sprintf("Page source revision %d\n\nActor: %s\n", revision, actor)), args...)
	if err != nil {
		return "", err
	}
	commit := strings.TrimSpace(string(out))
	if !ValidProjectCommit(commit) {
		return "", errors.New("invalid Git commit output")
	}
	// Content-addressed pins cannot move backwards during competing saves.
	_, err = runProjectGit(ctx, repo, nil, "update-ref", "refs/checkpoints/"+commit, commit)
	return commit, err
}

func (s *ProjectStore) ReadCheckpoint(ctx context.Context, ws, page, commit string) (*SourceProject, string, error) {
	if !ValidProjectCommit(commit) {
		return nil, "", errors.New("invalid Git commit")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	repo, err := s.gitPath(ctx, ws, page, false)
	if err != nil {
		return nil, "", err
	}
	raw, err := runProjectGit(ctx, repo, nil, "cat-file", "blob", commit+":source.yaml")
	if err != nil {
		return nil, "", err
	}
	source, err := ParseSourceProject(bytes.NewReader(raw))
	if err != nil {
		return nil, "", err
	}
	spec, err := runProjectGit(ctx, repo, nil, "cat-file", "blob", commit+":page.json")
	if err != nil {
		return nil, "", err
	}
	if len(spec) > MaxSpecBytes {
		return nil, "", errors.New("checkpoint spec exceeds limit")
	}
	return source, string(spec), nil
}
