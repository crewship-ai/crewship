package backup

// Checks a restore runs before anything changes: is there room, can this
// server read the bundle (directly or through a converter), can it run the
// crews it carries, which host settings must NOT come back on by themselves,
// and does the target collide with something.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"runtime"
	"sort"
	"strings"

	"filippo.io/age"
)

// RestoreChecks is the answer to POST …/backups/restore/checks.
type RestoreChecks struct {
	Space     SpaceCheck     `json:"space"`
	Format    FormatCheck    `json:"format"`
	Runtime   RuntimeCheck   `json:"runtime"`
	Unsafe    []string       `json:"unsafe"`
	Conflicts ConflictsCheck `json:"conflicts"`
	// Environments is one runtime check per complete container environment
	// the bundle carries: restore, rebuild (architecture or layers missing:
	// data back, environment rebuilt from the crew image) or skip (no
	// Docker). Always an array.
	Environments []EnvironmentCheck `json:"environments"`
}

// SpaceCheck: room on the data directory.
type SpaceCheck struct {
	OK        bool  `json:"ok"`
	NeedBytes int64 `json:"need_bytes"`
	FreeBytes int64 `json:"free_bytes"`
}

// FormatCheck: whether this server reads the bundle directly or through a
// converter first.
type FormatCheck struct {
	OK        bool `json:"ok"`
	Version   int  `json:"version"`
	Converter bool `json:"converter"`
}

// RuntimeCheck: Docker and architecture.
type RuntimeCheck struct {
	OK       bool     `json:"ok"`
	Detail   string   `json:"detail"`
	Warnings []string `json:"warnings"`
}

// ConflictsCheck: what the target already holds.
type ConflictsCheck struct {
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
}

// Restore targets (RestoreTarget in the UI model).
const (
	TargetEmptyServer  = "empty_server"
	TargetIsolated     = "isolated"
	TargetReplace      = "replace"
	TargetNewWorkspace = "new_workspace"
	TargetCrew         = "crew"
)

// ValidRestoreTarget reports whether t is a known target.
func ValidRestoreTarget(t string) bool {
	switch t {
	case TargetEmptyServer, TargetIsolated, TargetReplace, TargetNewWorkspace, TargetCrew:
		return true
	}
	return false
}

// RestoreCheckInput is what EvaluateRestoreChecks needs; the caller gathers
// the host facts.
type RestoreCheckInput struct {
	Manifest   *Manifest
	BundleSize int64
	Target     string
	FreeBytes  int64
	// DockerErr is nil when the daemon answered.
	DockerErr error
	// HostPlatform is os/arch of this server (default runtime.GOOS/GOARCH);
	// the Docker daemon's platform when it answered.
	HostPlatform string
	// DockerVersion is the daemon's version, when known.
	DockerVersion string
	// HasBlob reports whether an environment layer is available to this
	// server (inline in the bundle, or in the store beside it). Nil: none.
	HasBlob func(digest string) bool
	// Unsafe is what ScanUnsafeSettings found; UnsafeChecked says whether it
	// ran at all (it needs the key).
	Unsafe        []string
	UnsafeChecked bool
	// WorkspaceExists reports whether a workspace with this id or slug is on
	// the server.
	WorkspaceExists func(idOrSlug string) bool
}

// RestoreNeedBytes estimates the room a restore needs on the data directory.
// Instance bundles record exactly what they land; for the rest the payload
// expands, so three times the bundle is allowed.
func RestoreNeedBytes(m *Manifest, bundleSize int64) int64 {
	if m != nil && m.Contents.Instance != nil {
		n := m.Contents.Instance.DatabaseBytes + bundleSize
		for _, s := range m.Contents.Instance.Stores {
			n += s.Bytes
		}
		return n
	}
	return bundleSize * 3
}

// EvaluateRestoreChecks turns the gathered facts into the five checks.
func EvaluateRestoreChecks(in RestoreCheckInput) RestoreChecks {
	m := in.Manifest
	out := RestoreChecks{Unsafe: []string{}, Runtime: RuntimeCheck{Warnings: []string{}}, Environments: []EnvironmentCheck{}}
	need := RestoreNeedBytes(m, in.BundleSize)
	// Room for the environment layers that are loaded back.
	for _, e := range m.Contents.Environments {
		need += e.Bytes
	}
	out.Space = SpaceCheck{OK: in.FreeBytes >= need, NeedBytes: need, FreeBytes: in.FreeBytes}

	out.Format.Version = m.FormatVersion
	switch {
	case IsCompatible(m.FormatVersion):
		out.Format.OK = true
	case m.FormatVersion >= OldestRecoverableFormatVersion && m.FormatVersion < FormatVersion:
		out.Format.OK, out.Format.Converter = true, true
	}

	host := in.HostPlatform
	if host == "" {
		host = runtime.GOOS + "/" + runtime.GOARCH
	}
	crews := len(m.Contents.Crews)
	switch {
	case in.DockerErr == nil:
		out.Runtime.OK = true
		out.Runtime.Detail = "Docker is available on this server (" + host + ")"
	case crews == 0:
		out.Runtime.OK = true
		out.Runtime.Detail = "Docker is not available, and the bundle carries no crews that need it"
	default:
		out.Runtime.Detail = fmt.Sprintf("Docker is not available (%v): %d crew(s) restore without their environment until it is", in.DockerErr, crews)
	}
	withEnv := map[string]bool{}
	for _, e := range m.Contents.Environments {
		withEnv[e.Crew] = true
		c := CheckEnvironment(e, host, in.DockerErr, in.HasBlob)
		out.Environments = append(out.Environments, c)
		if c.Action == EnvActionRebuild {
			out.Runtime.Warnings = append(out.Runtime.Warnings, e.Crew+": "+c.Detail)
		}
	}
	if n := len(m.Contents.Environments); n > 0 && in.DockerErr == nil {
		v := "Docker"
		if in.DockerVersion != "" {
			v += " " + in.DockerVersion
		}
		out.Runtime.Detail = fmt.Sprintf("%s, %s for %d environment(s)", v, host, n)
	}
	if src := m.SourceInstance.Platform; src != "" && crews > 0 && archOf(src) != archOf(host) {
		for _, c := range m.Contents.Crews {
			if withEnv[c.Slug] {
				continue // its environment check above already says so
			}
			out.Runtime.Warnings = append(out.Runtime.Warnings, fmt.Sprintf("crew %s was built on %s and this server is %s: its environment is rebuilt from the crew image", c.Slug, src, host))
		}
	}
	if !in.UnsafeChecked && crews > 0 {
		out.Runtime.Warnings = append(out.Runtime.Warnings, "host settings in the crews' configuration were not checked: give the key to check them")
	}
	out.Unsafe = append(out.Unsafe, in.Unsafe...)

	ws := ""
	if m.Contents.Workspace != nil {
		ws = m.Contents.Workspace.Slug
	}
	exists := func(s string) bool { return s != "" && in.WorkspaceExists != nil && in.WorkspaceExists(s) }
	switch {
	case m.Scope == ScopeInstance && in.Target != TargetEmptyServer && in.Target != TargetIsolated:
		out.Conflicts = ConflictsCheck{Detail: "an instance backup restores only into an empty server (crewship recover) or an isolated drill (crewship backup drill)"}
	case in.Target == TargetEmptyServer:
		out.Conflicts = ConflictsCheck{OK: true, Detail: "runs offline with `crewship recover` into an empty data directory; nothing on this server changes"}
	case in.Target == TargetIsolated:
		out.Conflicts = ConflictsCheck{OK: true, Detail: "runs offline with `crewship backup drill` in a throwaway directory; nothing on this server changes"}
	case in.Target == TargetReplace:
		if exists(ws) {
			out.Conflicts = ConflictsCheck{OK: true, Detail: "replaces workspace " + ws + " on this server; its current data is overwritten"}
		} else {
			out.Conflicts = ConflictsCheck{OK: true, Detail: "workspace " + ws + " is not on this server; it is created"}
		}
	case in.Target == TargetNewWorkspace:
		if exists(ws) {
			out.Conflicts = ConflictsCheck{OK: true, Detail: "a workspace " + ws + " exists; the restore lands under the new name you give it"}
		} else {
			out.Conflicts = ConflictsCheck{OK: true, Detail: "no workspace has that name; nothing collides"}
		}
	case in.Target == TargetCrew:
		out.Conflicts = ConflictsCheck{OK: true, Detail: "restores one crew under the name you give it; no other crew changes"}
	default:
		out.Conflicts = ConflictsCheck{Detail: "unknown target " + in.Target}
	}
	return out
}

func archOf(platform string) string {
	if i := strings.LastIndex(platform, "/"); i >= 0 {
		return platform[i+1:]
	}
	return platform
}

// ScanUnsafeSettings decrypts the bundle and reads every crew's devcontainer
// configuration for host settings a restore must NOT switch back on by
// itself: a Docker socket mount, privileged mode, host path binds. For an
// instance bundle the per-workspace crew archives are read too.
func ScanUnsafeSettings(ctx context.Context, bundlePath string, identities []age.Identity, passphrase string) ([]string, error) {
	_, tr, closeAll, err := openInstancePayload(bundlePath, identities, passphrase)
	if err != nil {
		return nil, err
	}
	defer closeAll()
	found := map[string]bool{}
	if err := scanDevcontainers(ctx, tr, "", found); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(found))
	for s := range found {
		out = append(out, s)
	}
	sort.Strings(out)
	return out, nil
}

func scanDevcontainers(ctx context.Context, tr *TarZstReader, where string, found map[string]bool) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("backup: read payload: %w", err)
		}
		name := strings.TrimPrefix(hdr.Name, "./")
		switch {
		case strings.HasPrefix(name, "devcontainer/") && strings.HasSuffix(name, "/devcontainer.json"):
			data, err := io.ReadAll(io.LimitReader(tr, maxBackupDevcontainerEntryBytes))
			if err != nil {
				return err
			}
			crew := path.Base(path.Dir(name))
			if where != "" {
				crew = where + "/" + crew
			}
			for _, s := range UnsafeDevcontainerSettings(crew, data) {
				found[s] = true
			}
		case strings.HasPrefix(name, environmentsPrefix) && strings.HasSuffix(name, ".json") && !strings.Contains(strings.TrimPrefix(name, environmentsPrefix), "/"):
			// A complete environment's recorded container settings: what
			// was captured from `docker inspect` and is restored off.
			data, err := io.ReadAll(io.LimitReader(tr, maxEnvironmentRecordBytes))
			if err != nil {
				return err
			}
			var env Environment
			if err := json.Unmarshal(data, &env); err != nil {
				found["unreadable environment record "+name] = true
				continue
			}
			if where != "" {
				env.Crew = where + "/" + env.Crew
			}
			for _, s := range env.UnsafeSentences() {
				found[s] = true
			}
		case (strings.HasPrefix(name, instanceCrewsPrefix) || strings.HasPrefix(name, instanceEnvPrefix)) && strings.HasSuffix(name, instanceCrewsSuffix):
			inner, err := NewTarZstReader(tr)
			if err != nil {
				return err
			}
			ws := strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(name, instanceCrewsPrefix), instanceEnvPrefix), instanceCrewsSuffix)
			err = scanDevcontainers(ctx, inner, ws, found)
			_ = inner.Close()
			if err != nil {
				return err
			}
		}
	}
}

// UnsafeDevcontainerSettings lists the host-level settings in one crew's
// devcontainer.json, as sentences for the Recovery page ("Docker socket
// mount on ops"). Unparseable JSON is itself reported: nothing in it can be
// vouched for.
func UnsafeDevcontainerSettings(crew string, data []byte) []string {
	var doc map[string]any
	if err := json.Unmarshal(stripJSONComments(data), &doc); err != nil {
		return []string{"unreadable devcontainer configuration on " + crew}
	}
	seen := map[string]bool{}
	var out []string
	add := func(s string) {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	if b, ok := doc["privileged"].(bool); ok && b {
		add("privileged mode on " + crew)
	}
	for _, arg := range stringList(doc["runArgs"]) {
		switch {
		case arg == "--privileged":
			add("privileged mode on " + crew)
		case strings.Contains(arg, "docker.sock"):
			add("Docker socket mount on " + crew)
		case strings.HasPrefix(arg, "--network=host"), arg == "--pid=host", strings.HasPrefix(arg, "--cap-add"):
			add("host-level runtime option " + arg + " on " + crew)
		}
	}
	for _, m := range mountStrings(doc["mounts"]) {
		switch {
		case strings.Contains(m, "docker.sock"):
			add("Docker socket mount on " + crew)
		case strings.Contains(m, "type=bind"), strings.HasPrefix(m, "/"):
			add("host path mount on " + crew)
		}
	}
	return out
}

func stringList(v any) []string {
	arr, _ := v.([]any)
	var out []string
	for _, x := range arr {
		if s, ok := x.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// mountStrings flattens devcontainer mounts, which are strings
// ("source=/x,target=/y,type=bind") or objects ({source,target,type}).
func mountStrings(v any) []string {
	arr, _ := v.([]any)
	var out []string
	for _, x := range arr {
		switch m := x.(type) {
		case string:
			out = append(out, m)
		case map[string]any:
			src, _ := m["source"].(string)
			typ, _ := m["type"].(string)
			out = append(out, "source="+src+",type="+typ)
		}
	}
	return out
}

// stripJSONComments removes // line comments devcontainer.json allows
// (outside strings), which encoding/json refuses.
func stripJSONComments(b []byte) []byte {
	var out []byte
	inStr, esc := false, false
	for i := 0; i < len(b); i++ {
		c := b[i]
		if inStr {
			out = append(out, c)
			switch {
			case esc:
				esc = false
			case c == '\\':
				esc = true
			case c == '"':
				inStr = false
			}
			continue
		}
		if c == '"' {
			inStr = true
			out = append(out, c)
			continue
		}
		if c == '/' && i+1 < len(b) && b[i+1] == '/' {
			for i < len(b) && b[i] != '\n' {
				i++
			}
			if i < len(b) {
				out = append(out, '\n')
			}
			continue
		}
		out = append(out, c)
	}
	return out
}
