package sidecar

import (
	"context"
	"net/http"
)

type routineSourceRunKey struct{}
type routineSourceTokenKey struct{}

// routineOrigin admits only the authenticated execution identity. Tool JSON
// cannot select a source run or supply an origin receipt.
func (s *Server) routineOrigin(w http.ResponseWriter, r *http.Request) (context.Context, bool) {
	run, _, ok := s.issueOperationOrigin(w, r, "")
	if !ok {
		return r.Context(), false
	}
	ctx := context.WithValue(r.Context(), routineSourceRunKey{}, run)
	return context.WithValue(ctx, routineSourceTokenKey{}, bearerToken(r)), true
}
func routineSourceRun(ctx context.Context) string {
	id, _ := ctx.Value(routineSourceRunKey{}).(string)
	return id
}

func routineSourceToken(ctx context.Context) string {
	token, _ := ctx.Value(routineSourceTokenKey{}).(string)
	return token
}
