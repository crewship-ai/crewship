package manifest

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"

	"github.com/crewship-ai/crewship/internal/manifest/internalapi"
)

// internalClient adapts the existing *manifest.Client (which talks to
// the server via *http.Response) into the slim internalapi.Client
// interface that per-kind packages under internal/manifest/kinds
// depend on. Two reasons it exists:
//
//  1. The kinds package can't import internal/manifest directly without
//     creating a cycle (manifest imports kinds for dispatch).
//  2. Buffering the response body once here means every kind can read
//     it twice — once for "did this succeed?" and once for "decode the
//     payload" — without juggling Reader replay state.
//
// 16 MiB body cap is the runaway-response guard: manifest list
// endpoints return rows, not large blobs, and a buggy server pushing
// gigabytes would otherwise pin the CLI's memory.
type internalClient struct {
	inner *Client
}

func newInternalClient(c *Client) internalapi.Client { return &internalClient{inner: c} }

func (a *internalClient) Get(ctx context.Context, path string) (*internalapi.Response, error) {
	return wrapResponse(a.inner.api.Get(ctx, path))
}
func (a *internalClient) Post(ctx context.Context, path string, body any) (*internalapi.Response, error) {
	return wrapResponse(a.inner.api.Post(ctx, path, body))
}
func (a *internalClient) Patch(ctx context.Context, path string, body any) (*internalapi.Response, error) {
	return wrapResponse(a.inner.api.Patch(ctx, path, body))
}
func (a *internalClient) Delete(ctx context.Context, path string) (*internalapi.Response, error) {
	return wrapResponse(a.inner.api.Delete(ctx, path))
}

// Put falls through cli.Client.Do via a type-assertion when the
// underlying APIClient happens to expose Put; otherwise returns a
// loud error so a missing PUT surfaces actionably instead of
// degrading to "request silently dropped". Two SPEC-2 paths use
// PUT today (feature-flag override upsert, instance setting upsert).
func (a *internalClient) Put(ctx context.Context, path string, body any) (*internalapi.Response, error) {
	if putter, ok := a.inner.api.(interface {
		Put(ctx context.Context, path string, body any) (*http.Response, error)
	}); ok {
		return wrapResponse(putter.Put(ctx, path, body))
	}
	return nil, fmt.Errorf("manifest: HTTP PUT not supported by the underlying APIClient (%T) — extend APIClient.Put or use a different verb", a.inner.api)
}

func (a *internalClient) WorkspaceID() string { return a.inner.api.GetWorkspaceID() }

func wrapResponse(resp *http.Response, err error) (*internalapi.Response, error) {
	if err != nil {
		return nil, err
	}
	if resp == nil {
		return nil, nil
	}
	defer resp.Body.Close()
	const maxBody = 16 << 20
	limited := io.LimitReader(resp.Body, maxBody+1)
	data, readErr := io.ReadAll(limited)
	if readErr != nil {
		return nil, fmt.Errorf("read response body: %w", readErr)
	}
	if int64(len(data)) > maxBody {
		return nil, fmt.Errorf("response body exceeds %d-byte cap", maxBody)
	}
	return &internalapi.Response{
		StatusCode: resp.StatusCode,
		Body:       bytes.NewReader(data),
	}, nil
}
