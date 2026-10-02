package api

import (
	"context"
	"net/http"
	"testing"
)

// crewAwareInspector is a DockerInspector that also names the container's crew.
type crewAwareInspector struct {
	fakeDockerInspector
	owner string
}

func (f *crewAwareInspector) ContainerCrew(context.Context, string) (string, error) {
	return f.owner, nil
}

// A crew cannot expose another crew's container through crewshipd, which
// reaches every bridge including a crew's own isolated network (#2240).
func TestRequestExpose_RefusesAnotherCrewsContainer(t *testing.T) {
	for _, tc := range []struct {
		name  string
		owner string
		want  int
	}{
		{"own container", "crew1", http.StatusCreated},
		{"another crew's container", "crew2", http.StatusForbidden},
		{"unlabelled container", "", http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := newHandlerTestDB(t)
			planWorkspace(t, db, "ws1", "crew1", "agent1", "viktor")
			h := newRequestExposeHandler(t, db, DefaultPortExposeConfig(),
				&crewAwareInspector{fakeDockerInspector: fakeDockerInspector{ip: "10.231.0.2"}, owner: tc.owner})
			rec := postJSON(t, h.RequestExpose, map[string]any{
				"workspace_id": "ws1", "crew_id": "crew1", "agent_id": "agent1",
				"container_id": "c1", "port": 8000,
			})
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d; body=%s", rec.Code, tc.want, rec.Body.String())
			}
		})
	}
}
