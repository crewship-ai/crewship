package api

import (
	"context"
	"net/http"
	"strings"

	"github.com/crewship-ai/crewship/internal/runauthority"
)

type hostPipelineRunKey struct{}

func hostPipelineRunID(ctx context.Context) string {
	id, _ := ctx.Value(hostPipelineRunKey{}).(string)
	return id
}

// Must run inside requireInternal: only the authenticated host dispatcher may
// supply an origin for direct pipeline actions. Crew IPC cannot assert it.
func withHostChatOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pipelineRun := r.Header.Get("X-Crewship-Pipeline-Run")
		if pipelineRun != "" {
			if InternalTokenWorkspaceFromContext(r.Context()) != "" || InternalTokenCrewFromContext(r.Context()) != "" {
				replyError(w, 403, "pipeline_execution_requires_host")
				return
			}
			if len(pipelineRun) > 128 {
				replyError(w, 400, "pipeline_execution_invalid")
				return
			}
			r = r.WithContext(context.WithValue(r.Context(), hostPipelineRunKey{}, pipelineRun))
		}
		id := r.Header.Get("X-Crewship-Chat-Origin")
		if id != "" {
			if InternalTokenWorkspaceFromContext(r.Context()) != "" || InternalTokenCrewFromContext(r.Context()) != "" {
				replyError(w, 403, "human_origin_requires_host")
				return
			}
			if len(id) > 128 {
				replyError(w, 400, "human_origin_invalid")
				return
			}
			if strings.HasPrefix(r.URL.Path, "/api/v1/internal/issues") && r.Method != http.MethodGet && r.Method != http.MethodHead {
				part := strings.TrimPrefix(r.URL.Path, "/api/v1/internal/issues")
				supported := r.Method == http.MethodPost && (part == "" || part == "/authority" || strings.HasSuffix(part, "/comments") || strings.HasSuffix(part, "/show"))
				if r.Method == http.MethodPatch {
					identifier := strings.TrimPrefix(part, "/")
					supported = identifier != "" && !strings.Contains(identifier, "/")
				}
				if !supported {
					replyError(w, 403, "chat_issue_action_not_supported")
					return
				}
			}
			r = r.WithContext(runauthority.WithOriginID(r.Context(), id))
		}
		next.ServeHTTP(w, r)
	})
}
