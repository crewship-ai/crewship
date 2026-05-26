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
type skipTestGateClient struct {
	inner internalapi.Client
}

func withSkipTestGate(c internalapi.Client) internalapi.Client {
	return &skipTestGateClient{inner: c}
}

func (c *skipTestGateClient) Get(ctx context.Context, path string) (*internalapi.Response, error) {
	return c.inner.Get(ctx, path)
}

func (c *skipTestGateClient) Post(ctx context.Context, path string, body any) (*internalapi.Response, error) {
	if isPipelineSavePath(path) {
		body = mergeSkipTestGate(body)
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
func mergeSkipTestGate(body any) any {
	m, ok := body.(map[string]any)
	if !ok {
		return body
	}
	out := make(map[string]any, len(m)+1)
	for k, v := range m {
		out[k] = v
	}
	out["skip_test_gate"] = true
	return out
}
