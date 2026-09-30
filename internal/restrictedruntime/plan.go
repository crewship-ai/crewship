//go:build linux

// Package restrictedruntime is an opt-in, offline Docker prototype. It is not
// wired into production admission: only a trusted server Authority can supply
// plans. Unsupported profiles fail closed, never falling back to crew exec.
package restrictedruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"
)

var ErrDenied = errors.New("restricted runtime authority denied")
var identifier = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,95}$`)
var envName = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`)

// Authority must resolve an opaque server-issued handle against CURRENT state.
// Implementations must authenticate origins, fence generations and narrow
// delegated rights. Neither HTTP payloads nor agent metadata implement this API.
type Authority interface {
	Resolve(context.Context, string) (Plan, error)
	Secrets(context.Context, string) (map[string]string, error)
}

type Mount struct {
	Resource, Target string
	ReadOnly         bool
}
type Credential struct{ ID, Env, File string }
type Plan struct {
	Profile                                                                 string // empty is the existing offline prototype
	Network                                                                 *NetworkPlan
	NativeSandbox                                                           string               // exact host-owned native sandbox policy fingerprint
	NativeInputs                                                            *NativeInputManifest // exact explicitly selected host-frozen source snapshot
	Workspace, Principal, Agent, Scope, Attempt, Origin, OriginID, Revision string
	PrincipalKind                                                           string
	Generation                                                              uint64
	Mode                                                                    string
	Parent                                                                  string // opaque parent authority handle, never an agent-chosen identity
	Expires                                                                 time.Time
	Mounts                                                                  []Mount
	Credentials                                                             []Credential
	Command                                                                 []string
}

func (p Plan) validate(now time.Time) error {
	if p.NativeInputs != nil && p.NativeSandbox == "" {
		return ErrDenied
	}
	if err := p.validateNetwork(); err != nil {
		return err
	}
	for _, s := range []string{p.Workspace, p.Principal, p.Agent, p.Scope, p.Attempt, p.OriginID, p.Revision} {
		if !identifier.MatchString(s) {
			return ErrDenied
		}
	}
	if p.PrincipalKind != "human" && p.PrincipalKind != "service" {
		return ErrDenied
	}
	if p.Generation == 0 || p.Mode != "restricted" || !p.Expires.After(now) || p.Expires.Sub(now) > maxLease {
		return ErrDenied
	}
	switch p.Origin {
	case "chat", "routine", "webhook", "queue", "schedule":
	default:
		return ErrDenied
	}
	if len(p.Command) == 0 || len(p.Command) > 64 || p.Command[0] == "" || len(p.Mounts) > 16 || len(p.Credentials) > 16 {
		return ErrDenied
	}
	for _, arg := range p.Command {
		if strings.ContainsRune(arg, 0) || len(arg) > 64*1024 {
			return ErrDenied
		}
	}
	targets, resources := map[string]bool{}, map[string]bool{}
	for _, m := range p.Mounts {
		// No arbitrary paths: every mounted resource is a disjoint child of /data.
		if !identifier.MatchString(m.Resource) || path.Clean(m.Target) != m.Target || !strings.HasPrefix(m.Target, "/data/") || !identifier.MatchString(strings.TrimPrefix(m.Target, "/data/")) || targets[m.Target] || resources[m.Resource] {
			return ErrDenied
		}
		targets[m.Target], resources[m.Resource] = true, true
	}
	names, files, ids := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, c := range p.Credentials {
		if !identifier.MatchString(c.ID) || ids[c.ID] || (c.Env == "" && c.File == "") {
			return ErrDenied
		}
		ids[c.ID] = true
		if c.Env != "" {
			if !envName.MatchString(c.Env) || names[c.Env] || c.Env == "HOME" || c.Env == "PATH" || strings.HasPrefix(c.Env, "LD_") || strings.HasPrefix(c.Env, "XDG_") {
				return ErrDenied
			}
			names[c.Env] = true
		}
		if c.File != "" {
			if !identifier.MatchString(c.File) || files[c.File] {
				return ErrDenied
			}
			files[c.File] = true
		}
	}
	return nil
}

// fingerprint excludes only the renewable deadline. Any authority, mount,
// credential, task or generation change requires terminating and re-admitting.
func (p Plan) fingerprint() string {
	p.Expires = time.Time{}
	b, _ := json.Marshal(p)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// provenance binds derived data to all readable grants, independently of Revision.
func (p Plan) provenance() string {
	p.Attempt, p.Parent, p.Origin, p.OriginID = "", "", "", ""
	p.Generation = 0
	p.Command = nil
	p.Mounts = append([]Mount(nil), p.Mounts...)
	p.Credentials = append([]Credential(nil), p.Credentials...)
	sort.Slice(p.Mounts, func(i, j int) bool { return p.Mounts[i].Resource < p.Mounts[j].Resource })
	sort.Slice(p.Credentials, func(i, j int) bool { return p.Credentials[i].ID < p.Credentials[j].ID })
	return p.fingerprint()
}

// Narrow enforces the runtime portion of delegation. The application must also
// enforce its object/operation grants, origin permissions and data provenance.
func Narrow(parent, child Plan) error {
	if err := narrowNetwork(parent, child); err != nil {
		return err
	}
	if parent.Workspace != child.Workspace || parent.Principal != child.Principal || parent.PrincipalKind != child.PrincipalKind || parent.Scope != child.Scope || parent.Origin != child.Origin || parent.OriginID != child.OriginID || child.Expires.After(parent.Expires) {
		return ErrDenied
	}
	for _, c := range child.Mounts {
		found := false
		for _, p := range parent.Mounts {
			if c.Resource == p.Resource && c.Target == p.Target && (!p.ReadOnly || c.ReadOnly) {
				found = true
			}
		}
		if !found {
			return ErrDenied
		}
	}
	for _, c := range child.Credentials {
		found := false
		for _, p := range parent.Credentials {
			if c == p {
				found = true
			}
		}
		if !found {
			return ErrDenied
		}
	}
	return nil
}

func resolve(ctx context.Context, a Authority, handle string, seen map[string]bool) (Plan, error) {
	if handle == "" || seen[handle] || len(seen) >= 8 {
		return Plan{}, ErrDenied
	}
	seen[handle] = true
	p, err := a.Resolve(ctx, handle)
	if err != nil {
		return Plan{}, ErrDenied
	}
	if err = p.validate(time.Now()); err != nil {
		return Plan{}, err
	}
	if p.Parent != "" {
		parent, e := resolve(ctx, a, p.Parent, seen)
		if e != nil || Narrow(parent, p) != nil {
			return Plan{}, ErrDenied
		}
	}
	return p, nil
}

func secretValues(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for _, v := range m {
		if v != "" {
			out = append(out, v)
		}
	}
	sort.Slice(out, func(i, j int) bool { return len(out[i]) > len(out[j]) })
	return out
}
