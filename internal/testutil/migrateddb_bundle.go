package testutil

// A CI race bundle amortizes genuine migration work across isolated runners.
// It is an explicit alternative to the local cache, never a permissive variant
// of same-executable inheritance. A configured but invalid bundle fails closed.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"runtime/debug"
	"strconv"

	"github.com/crewship-ai/crewship/internal/database"
)

const (
	exportBundleEnv    = "CREWSHIP_TESTUTIL_EXPORT_BUNDLE"
	importBundleEnv    = "CREWSHIP_TESTUTIL_IMPORT_BUNDLE"
	bundleMetadataName = "template-metadata.json"
	bundleDatabaseName = "template.db"
)

type migratedBundleMetadata struct {
	Version              int    `json:"version"`
	SourceSHA            string `json:"source_sha"`
	RunID                string `json:"run_id"`
	RunAttempt           string `json:"run_attempt"`
	MigrationFingerprint string `json:"migration_fingerprint"`
	FixtureKey           string `json:"fixture_key"`
	GoVersion            string `json:"go_version"`
	GOOS                 string `json:"goos"`
	GOARCH               string `json:"goarch"`
	Race                 bool   `json:"race"`
	TemplateSize         int64  `json:"template_size"`
	TemplateSHA256       string `json:"template_sha256"`
}

func currentBundleIdentity() (migratedBundleMetadata, error) {
	m := migratedBundleMetadata{
		Version: 1, SourceSHA: os.Getenv("GITHUB_SHA"), RunID: os.Getenv("GITHUB_RUN_ID"),
		RunAttempt: os.Getenv("GITHUB_RUN_ATTEMPT"), GoVersion: runtime.Version(),
		GOOS: runtime.GOOS, GOARCH: runtime.GOARCH,
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, setting := range info.Settings {
			if setting.Key == "-race" && setting.Value == "true" {
				m.Race = true
			}
		}
	}
	if !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(m.SourceSHA) {
		return m, errors.New("race template requires an exact GITHUB_SHA")
	}
	if _, err := positiveBundleNumber(m.RunID); err != nil {
		return m, fmt.Errorf("race template GITHUB_RUN_ID: %w", err)
	}
	if _, err := positiveBundleNumber(m.RunAttempt); err != nil {
		return m, fmt.Errorf("race template GITHUB_RUN_ATTEMPT: %w", err)
	}
	var err error
	m.MigrationFingerprint, err = database.MigrationFingerprint()
	if err != nil {
		return m, err
	}
	root, err := findModuleRoot()
	if err != nil {
		return m, err
	}
	closure, err := sharedTemplateKey(root)
	if err != nil {
		return m, err
	}
	// Unlike the diagnostic local-cache opt-out, imported CI evidence always
	// binds both compiled migrations and their complete source dependency key.
	m.FixtureKey = m.MigrationFingerprint + "-" + closure
	return m, nil
}

func positiveBundleNumber(value string) (uint64, error) {
	if !regexp.MustCompile(`^[1-9][0-9]*$`).MatchString(value) {
		return 0, errors.New("expected a positive decimal identity")
	}
	number, err := strconv.ParseUint(value, 10, 64)
	return number, err
}

func validateBundleIdentity(got, want migratedBundleMetadata) error {
	if got.Version != 1 || got.Version != want.Version || !got.Race || !want.Race {
		return errors.New("race template requires version 1 and actual race instrumentation")
	}
	if got.SourceSHA != want.SourceSHA || got.RunID != want.RunID {
		return errors.New("race template source revision or workflow run differs")
	}
	oldAttempt, err := positiveBundleNumber(got.RunAttempt)
	if err != nil {
		return err
	}
	currentAttempt, err := positiveBundleNumber(want.RunAttempt)
	if err != nil || oldAttempt > currentAttempt {
		return errors.New("race template workflow attempt is invalid or from the future")
	}
	if got.MigrationFingerprint != want.MigrationFingerprint || got.FixtureKey != want.FixtureKey {
		return errors.New("race template migration fingerprint or source closure differs")
	}
	if got.GoVersion != want.GoVersion || got.GOOS != want.GOOS || got.GOARCH != want.GOARCH {
		return errors.New("race template Go version or platform differs")
	}
	if got.TemplateSize <= 0 || got.TemplateSize > 256<<20 {
		return errors.New("race template database size is invalid")
	}
	if digest, err := hex.DecodeString(got.TemplateSHA256); err != nil || len(digest) != sha256.Size || got.TemplateSHA256 != hex.EncodeToString(digest) {
		return errors.New("race template database digest is invalid")
	}
	return nil
}

// openBundleFile rejects symlink entries and replacement between inspection
// and opening. Hashing the bytes copied below also fences subsequent changes.
func openBundleFile(path string) (*os.File, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("race template entry must be a regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	actual, err := file.Stat()
	if err != nil || !os.SameFile(info, actual) {
		file.Close()
		return nil, errors.New("race template entry changed while opening")
	}
	return file, nil
}

func checkBundleDirectory(dir string) error {
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("race template bundle must be a directory without a symlink entry")
	}
	return nil
}

func importMigratedBundle(dir string) (string, error) {
	want, err := currentBundleIdentity()
	if err != nil {
		return "", err
	}
	return importMigratedBundleFor(dir, want)
}

func importMigratedBundleFor(dir string, want migratedBundleMetadata) (result string, err error) {
	if err := checkBundleDirectory(dir); err != nil {
		return "", err
	}
	metadata, err := openBundleFile(filepath.Join(dir, bundleMetadataName))
	if err != nil {
		return "", err
	}
	defer metadata.Close()
	metadataInfo, err := metadata.Stat()
	if err != nil || metadataInfo.Size() > 64<<10 {
		return "", errors.New("race template metadata exceeds size limit")
	}
	decoder := json.NewDecoder(io.LimitReader(metadata, 64<<10))
	decoder.DisallowUnknownFields()
	var got migratedBundleMetadata
	if err := decoder.Decode(&got); err != nil {
		return "", fmt.Errorf("race template metadata: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return "", errors.New("race template metadata has trailing content")
	}
	if err := validateBundleIdentity(got, want); err != nil {
		return "", err
	}
	source, err := openBundleFile(filepath.Join(dir, bundleDatabaseName))
	if err != nil {
		return "", err
	}
	defer source.Close()
	info, err := source.Stat()
	if err != nil || info.Size() != got.TemplateSize {
		return "", errors.New("race template database size differs")
	}
	privateDir, err := newMigratedTestDir()
	if err != nil {
		return "", err
	}
	defer func() {
		if err != nil {
			_ = os.RemoveAll(privateDir)
		}
	}()
	path := filepath.Join(privateDir, bundleDatabaseName)
	copy, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	count, copyErr := io.Copy(io.MultiWriter(copy, hash), io.LimitReader(source, got.TemplateSize+1))
	closeErr := copy.Close()
	if copyErr != nil || closeErr != nil || count != got.TemplateSize || hex.EncodeToString(hash.Sum(nil)) != got.TemplateSHA256 {
		return "", errors.New("race template database copy or SHA-256 verification failed")
	}
	if err := os.Chmod(path, 0o400); err != nil {
		return "", err
	}
	return path, nil
}

// Export calls the genuine migration engine on a new file, even if a usable
// local template already exists. CI charges that package's elapsed time once.
func exportMigratedBundle(dir string, identity migratedBundleMetadata) error {
	if !identity.Race {
		return errors.New("race template export requires an actual race build")
	}
	if err := checkBundleDirectory(dir); err != nil {
		return err
	}
	for _, name := range []string{bundleDatabaseName, bundleMetadataName} {
		if _, err := os.Lstat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			return errors.New("race template export refuses an existing destination")
		}
	}
	staging, err := os.CreateTemp(dir, ".template-building-")
	if err != nil {
		return err
	}
	path := staging.Name()
	if err := staging.Close(); err != nil {
		_ = os.Remove(path)
		return err
	}
	defer os.Remove(path)
	if err := migrateTemplateAt(path); err != nil {
		return err
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	hash := sha256.New()
	identity.TemplateSize, err = io.Copy(hash, file)
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	identity.TemplateSHA256 = hex.EncodeToString(hash.Sum(nil))
	if err := validateBundleIdentity(identity, identity); err != nil {
		return err
	}
	if err := os.Chmod(path, 0o400); err != nil {
		return err
	}
	// Link publishes without replacing another export, even on a concurrent
	// attempt. The staging file and destination live on the same filesystem.
	destination := filepath.Join(dir, bundleDatabaseName)
	if err := os.Link(path, destination); err != nil {
		return err
	}
	metadata, err := json.MarshalIndent(identity, "", "  ")
	if err != nil {
		_ = os.Remove(destination)
		return err
	}
	output, err := os.OpenFile(filepath.Join(dir, bundleMetadataName), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o400)
	if err != nil {
		_ = os.Remove(destination)
		return err
	}
	_, writeErr := output.Write(append(metadata, '\n'))
	closeErr = output.Close()
	if writeErr != nil || closeErr != nil {
		_ = os.Remove(destination)
		_ = os.Remove(filepath.Join(dir, bundleMetadataName))
		return errors.Join(writeErr, closeErr)
	}
	return nil
}
