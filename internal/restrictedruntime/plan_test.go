//go:build linux

package restrictedruntime

import (
	"context"
	"sync"
	"testing"
	"time"
)

type catalogMap map[string]string

func (c catalogMap) Volume(_ context.Context, _ Plan, m Mount) (string, error) {
	v, ok := c[m.Resource]
	if !ok {
		return "", ErrDenied
	}
	return v, nil
}

func testPlan() Plan {
	return Plan{Workspace: "w1", Principal: "h1", PrincipalKind: "human", Agent: "a", Scope: "h1-chat1", Attempt: "attempt1", Origin: "chat", OriginID: "chat1", Revision: "r1", Generation: 1, Mode: "restricted", Expires: time.Now().Add(10 * time.Second), Mounts: []Mount{{"private", "/data/private", false}, {"shared", "/data/shared", true}}, Credentials: []Credential{{"direct", "DIRECT_TOKEN", "direct"}}, Command: []string{"sh", "-c", "sleep 3600"}}
}
func TestPlanRejectsUnsafeInputs(t *testing.T) {
	tests := map[string]func(*Plan){
		"legacy fallback": func(p *Plan) { p.Mode = "" }, "unknown principal kind": func(p *Plan) { p.PrincipalKind = "client-says-owner" },
		"missing principal": func(p *Plan) { p.Principal = "" }, "path identity": func(p *Plan) { p.Scope = "../h2" }, "unknown origin": func(p *Plan) { p.Origin = "client-says-owner" }, "expired": func(p *Plan) { p.Expires = time.Now().Add(-time.Second) }, "long lease": func(p *Plan) { p.Expires = time.Now().Add(time.Minute) }, "raw mount": func(p *Plan) { p.Mounts[0].Target = "/var/run/docker.sock" }, "traversal": func(p *Plan) { p.Mounts[0].Target = "/data/a/../b" }, "nested mount": func(p *Plan) { p.Mounts[0].Target = "/data/shared/nested" }, "writable alias": func(p *Plan) { p.Mounts = append(p.Mounts, Mount{"shared", "/data/alias", false}) }, "duplicate target": func(p *Plan) { p.Mounts[0].Target = p.Mounts[1].Target }, "secret traversal": func(p *Plan) { p.Credentials[0].File = "../broker/key" }, "env loader": func(p *Plan) { p.Credentials[0].Env = "LD_PRELOAD" }, "duplicate secret": func(p *Plan) { p.Credentials = append(p.Credentials, p.Credentials[0]) }}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			p := testPlan()
			mutate(&p)
			if p.validate(time.Now()) == nil {
				t.Fatal("unsafe plan accepted")
			}
		})
	}
	if e := testPlan().validate(time.Now()); e != nil {
		t.Fatal(e)
	}
}
func TestDelegationOnlyNarrows(t *testing.T) {
	p := testPlan()
	c := p
	c.Agent = "b"
	c.Attempt = "child"
	c.Mounts = []Mount{{"shared", "/data/shared", true}}
	c.Credentials = nil
	c.Expires = p.Expires.Add(-time.Second)
	if e := Narrow(p, c); e != nil {
		t.Fatal(e)
	}
	for name, mutate := range map[string]func(*Plan){"write": func(c *Plan) { c.Mounts[0].ReadOnly = false }, "new resource": func(c *Plan) { c.Mounts[0].Resource = "foreign" }, "new secret": func(c *Plan) { c.Credentials = []Credential{{"foreign", "SECRET", ""}} }, "principal kind": func(c *Plan) { c.PrincipalKind = "service" },
		"principal": func(c *Plan) { c.Principal = "h2" }, "audience": func(c *Plan) { c.Scope = "h2-chat" }, "longer lease": func(c *Plan) { c.Expires = p.Expires.Add(time.Second) }} {
		t.Run(name, func(t *testing.T) {
			v := c
			v.Mounts = append([]Mount(nil), c.Mounts...)
			mutate(&v)
			if Narrow(p, v) == nil {
				t.Fatal("delegation widened rights")
			}
		})
	}
}

type fixtureAuthority struct {
	mu          sync.Mutex
	plans       map[string]Plan
	secrets     map[string]map[string]string
	denied      map[string]bool
	ttl         time.Duration
	onSecrets   func()
	unavailable bool
}

func (a *fixtureAuthority) Resolve(_ context.Context, h string) (Plan, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.unavailable {
		return Plan{}, context.DeadlineExceeded
	}
	p, ok := a.plans[h]
	if !ok || a.denied[h] {
		return Plan{}, ErrDenied
	}
	p.Expires = time.Now().Add(a.ttl)
	return p, nil
}
func (a *fixtureAuthority) Secrets(_ context.Context, h string) (map[string]string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.onSecrets != nil {
		a.onSecrets()
	}
	if a.denied[h] {
		return nil, ErrDenied
	}
	out := map[string]string{}
	for k, v := range a.secrets[h] {
		out[k] = v
	}
	return out, nil
}
func (a *fixtureAuthority) revoke(h string) { a.mu.Lock(); defer a.mu.Unlock(); a.denied[h] = true }
func TestServerHandlesAndOrigins(t *testing.T) {
	a := &fixtureAuthority{plans: map[string]Plan{}, denied: map[string]bool{}, ttl: 10 * time.Second}
	for _, origin := range []string{"chat", "routine", "webhook", "queue", "schedule"} {
		p := testPlan()
		p.Origin = origin
		a.plans[origin] = p
		if _, e := resolve(context.Background(), a, origin, map[string]bool{}); e != nil {
			t.Fatal(e)
		}
	}
	for _, h := range []string{"", `{"principal":"owner","policy_version":"r999"}`, "h2"} {
		if _, e := resolve(context.Background(), a, h, map[string]bool{}); e == nil {
			t.Fatal("forged handle accepted")
		}
	}
	p := testPlan()
	p.Parent = "parent"
	a.plans["child"] = p
	a.plans["parent"] = testPlan()
	a.revoke("parent")
	if _, e := resolve(context.Background(), a, "child", map[string]bool{}); e == nil {
		t.Fatal("revoked parent accepted")
	}
	a.denied["parent"] = false
	p.Parent = "child"
	a.plans["child"] = p
	if _, e := resolve(context.Background(), a, "child", map[string]bool{}); e == nil {
		t.Fatal("cycle accepted")
	}
}

func TestOutputWithholdsSecretSplitAcrossWrites(t *testing.T) {
	p := testPlan()
	a := &fixtureAuthority{plans: map[string]Plan{"h": p}, denied: map[string]bool{}, ttl: 10 * time.Second}
	s := &Session{manager: &Manager{Authority: a}, handle: "h", plan: p, secrets: []string{"synthetic-whole-secret"}}
	_, _ = s.Write([]byte("log synthetic-wh"))
	out, e := s.Output(context.Background())
	if e != nil || out != "log [REDACTED_PARTIAL]" {
		t.Fatalf("partial output was not withheld: %v", e)
	}
	_, _ = s.Write([]byte("ole-secret end"))
	out, e = s.Output(context.Background())
	if e != nil || out != "log [REDACTED] end" {
		t.Fatal("split secret was not redacted")
	}
	a.revoke("h")
	if _, e = s.Output(context.Background()); e == nil {
		t.Fatal("revoked output allowed")
	}
}
func TestProvenanceIncludesRightsWithoutRevisionChange(t *testing.T) {
	p := testPlan()
	q := p
	q.Attempt = "retry"
	q.Generation++
	q.Command = []string{"another-task"}
	if p.provenance() != q.provenance() {
		t.Fatal("attempt changed data provenance")
	}
	q.Credentials = nil
	if p.provenance() == q.provenance() {
		t.Fatal("credential removal failed to quarantine old state")
	}
	q = p
	q.Mounts = []Mount{{"private", "/data/private", false}}
	if p.provenance() == q.provenance() {
		t.Fatal("data revocation failed to quarantine old state")
	}
}

func TestCloseSurfacesUnconfirmedTermination(t *testing.T) {
	s := &Session{record: Record{Status: "termination_unconfirmed"}}
	s.once.Do(func() {})
	m := &Manager{sessions: map[string]*Session{"old": s}}
	if m.Close() == nil {
		t.Fatal("close reported a successful drain for an unconfirmed process")
	}
}
