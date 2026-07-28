package manifest

import (
	"context"
	"strings"

	"github.com/crewship-ai/crewship/internal/manifest/internalapi"
)

// skipTestGateClient decorates an internalapi.Client to inject
// `skip_test_gate: true` into the body of every POST to
// `/pipelines/save`. The server's pipelines_crud.Save handler accepts
// this flag for OWNER/ADMIN callers (see internal/api/pipelines_crud.go);
// MANAGER+ without the flag must produce a fresh passing test_run
// within 5 minutes, which the manifest layer has no way to drive (it
// would need to invoke the routine before saving it, and the routine
// might depend on credentials that are still PENDING from the same
// apply). Forwarding the flag turns "first apply 422s on a brand-new
// routine" into a clean Plan + Apply.
//
// Scope is intentionally narrow: only the `save` endpoint gets the
// field. Sibling routine endpoints (schedules, webhooks, run, dry_run)
// either don't accept the field or interpret it differently, and a
// blanket inject would leak the OWNER/ADMIN escape hatch into places
// it isn't meant to apply.
//
// The governance gate is the second half of the same story. A routine with an
// http/egress or code step is classified RISKY and lands as `proposed`, which
// cannot run until a MANAGER+ approves it. That is right for a routine an
// agent wrote; it is wrong for a seed, where the whole promise is "fill in the
// environment file and every routine runs". So the same OWNER/ADMIN escape
// hatch the server already exposes (`skip_governance_gate`) is forwarded the
// same way, behind its own flag — the operator has to ask for it, and asking
// for one gate does not silently open the other.
type skipTestGateClient struct {
	inner internalapi.Client
	// governance forwards skip_governance_gate alongside skip_test_gate.
	governance bool
}

func withSkipTestGate(c internalapi.Client) internalapi.Client {
	return &skipTestGateClient{inner: c}
}

// withSkipGovernanceGate forwards skip_governance_gate on routine saves, so a
// risky-but-intended routine lands `active` instead of queued for approval.
func withSkipGovernanceGate(c internalapi.Client) internalapi.Client {
	if existing, ok := c.(*skipTestGateClient); ok {
		existing.governance = true
		return existing
	}
	return &skipTestGateClient{inner: c, governance: true}
}

func (c *skipTestGateClient) Get(ctx context.Context, path string) (*internalapi.Response, error) {
	return c.inner.Get(ctx, path)
}

func (c *skipTestGateClient) Post(ctx context.Context, path string, body any) (*internalapi.Response, error) {
	if isPipelineSavePath(path) {
		body = mergeSkipTestGate(body, c.governance)
	}
	return c.inner.Post(ctx, path, body)
}

func (c *skipTestGateClient) Patch(ctx context.Context, path string, body any) (*internalapi.Response, error) {
	return c.inner.Patch(ctx, path, body)
}

func (c *skipTestGateClient) Put(ctx context.Context, path string, body any) (*internalapi.Response, error) {
	return c.inner.Put(ctx, path, body)
}

func (c *skipTestGateClient) Delete(ctx context.Context, path string) (*internalapi.Response, error) {
	return c.inner.Delete(ctx, path)
}

func (c *skipTestGateClient) WorkspaceID() string {
	return c.inner.WorkspaceID()
}

// isPipelineSavePath matches both legacy `/pipelines/save` and the
// workspace-scoped `/api/v1/workspaces/{ws}/pipelines/save` shape. We
// stay loose because the router has carried both in the past and a
// future API version might shift the prefix again.
func isPipelineSavePath(path string) bool {
	return strings.HasSuffix(path, "/pipelines/save")
}

// mergeSkipTestGate returns a copy of body with skip_test_gate=true
// added. Bodies that aren't map[string]any pass through unchanged —
// every caller in the manifest layer uses maps today, and silently
// hijacking some other body shape would be more surprising than the
// flag failing to apply.
//
// Capacity hint is len(m) (not len(m)+1) to keep CodeQL's "size
// computation for allocation may overflow" gate quiet. Go grows the
// map on the one extra Set call below, so the runtime cost is the
// same one bucket reallocation either way.
func mergeSkipTestGate(body any, governance bool) any {
	m, ok := body.(map[string]any)
	if !ok {
		return body
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	out["skip_test_gate"] = true
	if governance {
		out["skip_governance_gate"] = true
	}
	return out
}
