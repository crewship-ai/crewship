package craftassets

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Store is the directory installed packages live in.
type Store struct{ Root string }

func (s Store) dir(pin Pin) string { return filepath.Join(s.Root, pin.Application, pin.ManifestSHA256) }

// Open returns the installed package for pin. It verifies the manifest
// against the pin; files are verified whenever they are read.
func (s Store) Open(pin Pin) (*Installed, error) {
	if s.Root == "" {
		return nil, ErrNotInstalled
	}
	dir := s.dir(pin)
	f, err := os.Open(filepath.Join(dir, manifestName))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrNotInstalled
	}
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrIntegrity, err)
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, maxManifest+1))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrIntegrity, err)
	}
	m, err := parseManifest(raw, pin)
	if err != nil {
		return nil, err
	}
	return &Installed{Pin: pin, Toolchain: m.Toolchain, Component: m.Component, Dir: dir, files: os.DirFS(dir)}, nil
}

// Sweep removes what an interrupted download or installation left in the
// store root (.download-* files, .staging-* directories). Installed packages
// are never touched. Call it at startup, before any installation runs.
func (s Store) Sweep() error {
	if s.Root == "" {
		return nil
	}
	entries, err := os.ReadDir(s.Root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var errs []error
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".download-") || strings.HasPrefix(e.Name(), ".staging-") {
			errs = append(errs, os.RemoveAll(filepath.Join(s.Root, e.Name())))
		}
	}
	return errors.Join(errs...)
}

// Install unpacks a package (a tar archive) into the store, verifies every
// file against the manifest the pin names, and only then makes it visible
// under <application>/<pin>/. A package already installed and intact is kept.
func (s Store) Install(pin Pin, archive io.Reader) (*Installed, error) {
	if s.Root == "" {
		return nil, errors.New("no directory for Craft application components is configured")
	}
	if existing, err := s.Open(pin); err == nil {
		if err = verifyAll(existing); err == nil {
			return existing, nil
		}
	}
	if err := os.MkdirAll(filepath.Join(s.Root, pin.Application), 0o750); err != nil {
		return nil, err
	}
	staging, err := os.MkdirTemp(s.Root, ".staging-"+pin.Application+"-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(staging)
	if err = unpack(archive, staging); err != nil {
		return nil, err
	}
	staged, err := openDir(pin, staging)
	if err != nil {
		return nil, err
	}
	if err = exactly(staging, staged); err != nil {
		return nil, err
	}
	if err = verifyAll(staged); err != nil {
		return nil, err
	}
	final := s.dir(pin)
	// A damaged earlier copy is replaced; Open verified it is not intact.
	if err = os.RemoveAll(final); err != nil {
		return nil, err
	}
	if err = os.Rename(staging, final); err != nil {
		return nil, err
	}
	return s.Open(pin)
}

// openDir reads a package unpacked directly in dir (not in the store layout).
func openDir(pin Pin, dir string) (*Installed, error) {
	raw, err := os.ReadFile(filepath.Join(dir, manifestName))
	if err != nil {
		return nil, fmt.Errorf("%w: package has no manifest", ErrIntegrity)
	}
	m, err := parseManifest(raw, pin)
	if err != nil {
		return nil, err
	}
	return &Installed{Pin: pin, Toolchain: m.Toolchain, Component: m.Component, Dir: dir, files: os.DirFS(dir)}, nil
}

func verifyAll(i *Installed) error {
	for _, f := range (Manifest{Component: i.Component}).files() {
		if err := verified(i.files, f, io.Discard); err != nil {
			return fmt.Errorf("%w: %s", err, f.Path)
		}
	}
	return nil
}

// exactly refuses a package that holds files its manifest does not name.
func exactly(dir string, i *Installed) error {
	want := map[string]bool{manifestName: true}
	for _, f := range (Manifest{Component: i.Component}).files() {
		want[f.Path] = true
	}
	return filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		if !want[filepath.ToSlash(rel)] {
			return fmt.Errorf("%w: unexpected file %s", ErrIntegrity, rel)
		}
		return nil
	})
}

// unpack writes the regular files of a tar archive below dir. Anything else
// (links, devices, absolute or escaping names, repeats) refuses the package.
func unpack(archive io.Reader, dir string) error {
	tr := tar.NewReader(io.LimitReader(archive, packageBytes+1))
	seen := map[string]bool{}
	var total int64
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("%w: not a package: %v", ErrIntegrity, err)
		}
		if h.Typeflag == tar.TypeDir {
			continue
		}
		name := strings.TrimPrefix(h.Name, "./")
		if h.Typeflag != tar.TypeReg || !pathPattern.MatchString(name) || !fs.ValidPath(name) || seen[name] || len(seen) >= packageFiles {
			return fmt.Errorf("%w: invalid package entry %q", ErrIntegrity, h.Name)
		}
		seen[name] = true
		total += h.Size
		if h.Size < 0 || total > packageBytes {
			return fmt.Errorf("%w: package too large", ErrIntegrity)
		}
		target := filepath.Join(dir, filepath.FromSlash(name))
		if err = os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
			return fmt.Errorf("%w: %v", ErrIntegrity, err)
		}
		out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o440)
		if err != nil {
			return fmt.Errorf("%w: %v", ErrIntegrity, err)
		}
		_, err = io.Copy(out, io.LimitReader(tr, h.Size))
		if cerr := out.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return fmt.Errorf("%w: %v", ErrIntegrity, err)
		}
	}
	if !seen[manifestName] {
		return fmt.Errorf("%w: package has no manifest", ErrIntegrity)
	}
	return nil
}

// Download fetches the application's package from the catalog's URL into
// the store and installs it.
func (s Store) Download(ctx context.Context, c Catalog, application string, client *http.Client) (*Installed, error) {
	pin, ok := c.Pins[application]
	if !ok {
		return nil, ErrNotPublished
	}
	address := c.URL(application)
	if address == "" {
		return nil, errors.New("this Crewship build has no download address for Craft components; install from a file")
	}
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Minute}
	}
	if err := os.MkdirAll(s.Root, 0o750); err != nil {
		return nil, err
	}
	tmp, err := os.CreateTemp(s.Root, ".download-"+application+"-")
	if err != nil {
		return nil, err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", address, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download %s: HTTP %d", address, resp.StatusCode)
	}
	n, err := io.Copy(tmp, io.LimitReader(resp.Body, packageBytes+1))
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", address, err)
	}
	if n > packageBytes {
		return nil, fmt.Errorf("%w: package too large", ErrIntegrity)
	}
	if _, err = tmp.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	return s.Install(pin, tmp)
}
