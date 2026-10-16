package api

import (
	"context"
	"net/http/httptest"
	"testing"
)

func TestDocumentBlockSelectorIsOnlyCurrentRead(t *testing.T) {
	for _, tc := range []struct {
		name, method, path, checkpoint string
		want                           bool
		invalid                        bool
	}{
		{"legacy read", "GET", "/api/v1/pages/report/document?workspace_id=ws", "", false, false},
		{"current block", "GET", "/api/v1/pages/report/document?block_id=block_1&workspace_id=ws", "", true, false},
		{"protected block", "GET", "/api/v1/internal/documents/report?block_id=block_1", "", true, false},
		{"duplicate", "GET", "/api/v1/pages/report/document?block_id=a&block_id=b", "", true, true},
		{"empty", "GET", "/api/v1/pages/report/document?block_id=", "", true, true},
		{"escaped path", "GET", "/api/v1/pages/report/document?block_id=..%2Fother", "", true, true},
		{"oversized ID", "GET", "/api/v1/pages/report/document?block_id=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "", true, true},
		{"unknown query", "GET", "/api/v1/pages/report/document?block_id=a&atRev=1", "", true, true},
		{"wrong workspace", "GET", "/api/v1/pages/report/document?block_id=a&workspace_id=other", "", true, true},
		{"mutation", "POST", "/api/v1/pages/report/document/patch?block_id=a", "", true, true},
		{"history", "GET", "/api/v1/pages/report/document/checkpoints/pin?block_id=a", "pin", true, true},
		{"comments", "GET", "/api/v1/pages/report/document/comments?block_id=a", "", true, true},
		{"protected mutation", "POST", "/api/v1/internal/documents/report/patch?block_id=a", "", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, tc.path, nil)
			r = r.WithContext(context.WithValue(r.Context(), ctxWorkspaceID, "ws"))
			r.SetPathValue("checkpoint", tc.checkpoint)
			_, present, err := documentBlockSelector(r)
			if present != tc.want || (err != nil) != tc.invalid {
				t.Fatalf("present=%v err=%v", present, err)
			}
		})
	}
}
