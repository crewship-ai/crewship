package kinds

// Coverage-focused tests for crew.go. Reuses the scriptable covClient
// fake from routine_cov_test.go. crew_test.go owns the happy paths;
// this file pins updatePatch field coverage, the JSON renderers'
// edge branches, listCrewRemotes decode shapes, parse round-trips,
// and the jsonStringEqual normalisation matrix.

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/manifest/internalapi"
)

func crewStr(s string) *string { return &s }

// ── jsonStringEqual ─────────────────────────────────────────────────

func TestCrewCov_JSONStringEqual(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		a, b string
		want bool
	}{
		{"identical strings", `{"a":1}`, `{"a":1}`, true},
		{"both empty are identical", "", "", true},
		{"a empty", "", `{"a":1}`, false},
		{"b empty", `{"a":1}`, "  ", false},
		{"a invalid json", `{nope`, `{"a":1}`, false},
		{"b invalid json", `{"a":1}`, `nope}`, false},
		{"key order normalised", `{"a":1,"b":2}`, `{"b":2,"a":1}`, true},
		{"whitespace normalised", `{ "a" : 1 }`, `{"a":1}`, true},
		{"different values", `{"a":1}`, `{"a":2}`, false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := jsonStringEqual(tc.a, tc.b); got != tc.want {
				t.Fatalf("jsonStringEqual(%q,%q) = %t, want %t", tc.a, tc.b, got, tc.want)
			}
		})
	}
}

// ── JSON renderers ──────────────────────────────────────────────────

func TestCrewCov_DevcontainerJSON(t *testing.T) {
	t.Parallel()

	t.Run("nil devcontainer", func(t *testing.T) {
		t.Parallel()
		d := &CrewDocument{}
		got, err := d.devcontainerJSON()
		if got != "" || err != nil {
			t.Fatalf("got (%q,%v)", got, err)
		}
	})
	t.Run("empty block renders empty", func(t *testing.T) {
		t.Parallel()
		d := &CrewDocument{Spec: CrewSpec{Devcontainer: &Devcontainer{}}}
		got, err := d.devcontainerJSON()
		if got != "" || err != nil {
			t.Fatalf("got (%q,%v)", got, err)
		}
	})
	t.Run("typed fields win over raw and runtime image wins over devcontainer image", func(t *testing.T) {
		t.Parallel()
		d := &CrewDocument{Spec: CrewSpec{
			RuntimeImage: "spec-image:1",
			Devcontainer: &Devcontainer{
				Image:             "nested-image:2",
				Features:          map[string]any{"ghcr.io/devcontainers/features/common-utils:2": map[string]any{}},
				Env:               map[string]string{"NODE_ENV": "production"},
				PostCreateCommand: "make setup",
				MemoryMB:          4096,
				CPUs:              2.5,
				Raw:               map[string]any{"image": "raw-image:3", "remoteUser": "agent"},
			},
		}}
		got, err := d.devcontainerJSON()
		if err != nil {
			t.Fatalf("devcontainerJSON: %v", err)
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(got), &m); err != nil {
			t.Fatalf("output not JSON: %v", err)
		}
		if m["image"] != "spec-image:1" {
			t.Fatalf("image = %v (typed/spec must win)", m["image"])
		}
		if m["remoteUser"] != "agent" {
			t.Fatalf("raw passthrough lost: %v", m)
		}
		if m["postCreateCommand"] != "make setup" {
			t.Fatalf("postCreateCommand = %v", m["postCreateCommand"])
		}
		hr, _ := m["hostRequirements"].(map[string]any)
		if hr == nil || hr["memory"] != "4096mb" || hr["cpus"] != 2.5 {
			t.Fatalf("hostRequirements = %v", hr)
		}
		env, _ := m["containerEnv"].(map[string]any)
		if env == nil || env["NODE_ENV"] != "production" {
			t.Fatalf("containerEnv = %v", env)
		}
	})
	t.Run("devcontainer image used when spec image empty", func(t *testing.T) {
		t.Parallel()
		d := &CrewDocument{Spec: CrewSpec{Devcontainer: &Devcontainer{Image: "nested:1"}}}
		got, err := d.devcontainerJSON()
		if err != nil {
			t.Fatalf("devcontainerJSON: %v", err)
		}
		if !strings.Contains(got, `"image":"nested:1"`) {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("cpus only host requirements", func(t *testing.T) {
		t.Parallel()
		d := &CrewDocument{Spec: CrewSpec{Devcontainer: &Devcontainer{CPUs: 1.5}}}
		got, err := d.devcontainerJSON()
		if err != nil || !strings.Contains(got, `"cpus":1.5`) || strings.Contains(got, "memory") {
			t.Fatalf("got (%q,%v)", got, err)
		}
	})
}

func TestCrewCov_MiseJSON(t *testing.T) {
	t.Parallel()

	t.Run("nil mise", func(t *testing.T) {
		t.Parallel()
		d := &CrewDocument{}
		got, err := d.miseJSON()
		if got != "" || err != nil {
			t.Fatalf("got (%q,%v)", got, err)
		}
	})
	t.Run("empty block renders empty", func(t *testing.T) {
		t.Parallel()
		d := &CrewDocument{Spec: CrewSpec{Mise: &MiseConfig{}}}
		got, err := d.miseJSON()
		if got != "" || err != nil {
			t.Fatalf("got (%q,%v)", got, err)
		}
	})
	t.Run("tools plus raw", func(t *testing.T) {
		t.Parallel()
		d := &CrewDocument{Spec: CrewSpec{Mise: &MiseConfig{
			Tools: map[string]string{"node": "22"},
			Raw:   map[string]any{"env": map[string]any{"FOO": "bar"}},
		}}}
		got, err := d.miseJSON()
		if err != nil {
			t.Fatalf("miseJSON: %v", err)
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(got), &m); err != nil {
			t.Fatalf("not JSON: %v", err)
		}
		tools, _ := m["tools"].(map[string]any)
		if tools == nil || tools["node"] != "22" {
			t.Fatalf("tools = %v", tools)
		}
		if m["env"] == nil {
			t.Fatalf("raw passthrough lost: %v", m)
		}
	})
}

func TestCrewCov_ServicesJSON(t *testing.T) {
	t.Parallel()

	t.Run("empty services", func(t *testing.T) {
		t.Parallel()
		d := &CrewDocument{}
		got, err := d.servicesJSON()
		if got != "" || err != nil {
			t.Fatalf("got (%q,%v)", got, err)
		}
	})
	t.Run("sorted by name", func(t *testing.T) {
		t.Parallel()
		d := &CrewDocument{Spec: CrewSpec{Services: []Service{
			{Name: "redis", Image: "redis:7"},
			{Name: "postgres", Image: "postgres:16"},
		}}}
		got, err := d.servicesJSON()
		if err != nil {
			t.Fatalf("servicesJSON: %v", err)
		}
		var svcs []Service
		if err := json.Unmarshal([]byte(got), &svcs); err != nil {
			t.Fatalf("not JSON: %v", err)
		}
		if len(svcs) != 2 || svcs[0].Name != "postgres" || svcs[1].Name != "redis" {
			t.Fatalf("order = %v", svcs)
		}
		// Source slice must stay untouched.
		if d.Spec.Services[0].Name != "redis" {
			t.Fatalf("source slice mutated: %v", d.Spec.Services)
		}
	})
}

// ── createBody / updatePatch ────────────────────────────────────────

func TestCrewCov_CreateBody(t *testing.T) {
	t.Parallel()

	t.Run("spec description fallback", func(t *testing.T) {
		t.Parallel()
		d := &CrewDocument{
			Metadata: internalapi.Metadata{Name: "Eng", Slug: "eng"},
			Spec:     CrewSpec{Description: "from spec", RuntimeImage: "img:1"},
		}
		body, err := d.createBody()
		if err != nil {
			t.Fatalf("createBody: %v", err)
		}
		if body["description"] != "from spec" {
			t.Fatalf("body = %v", body)
		}
	})
	t.Run("declared empty services stores literal empty array", func(t *testing.T) {
		t.Parallel()
		d := &CrewDocument{
			Metadata: internalapi.Metadata{Name: "Eng", Slug: "eng"},
			Spec:     CrewSpec{RuntimeImage: "img:1", Services: []Service{}},
		}
		body, err := d.createBody()
		if err != nil {
			t.Fatalf("createBody: %v", err)
		}
		if body["services_json"] != "[]" {
			t.Fatalf("services_json = %v", body["services_json"])
		}
	})
	t.Run("full body with sizing", func(t *testing.T) {
		t.Parallel()
		d := &CrewDocument{
			Metadata: internalapi.Metadata{Name: "Eng", Slug: "eng", Description: "meta"},
			Spec: CrewSpec{
				RuntimeImage: "img:1",
				Color:        "#112233",
				Icon:         "terminal",
				Devcontainer: &Devcontainer{MemoryMB: 2048, CPUs: 2},
				Mise:         &MiseConfig{Tools: map[string]string{"node": "22"}},
				Services:     []Service{{Name: "db", Image: "postgres:16"}},
			},
		}
		body, err := d.createBody()
		if err != nil {
			t.Fatalf("createBody: %v", err)
		}
		if body["description"] != "meta" || body["color"] != "#112233" || body["icon"] != "terminal" {
			t.Fatalf("body = %v", body)
		}
		if body["container_memory_mb"] != 2048 || body["container_cpus"] != 2.0 {
			t.Fatalf("sizing = %v / %v", body["container_memory_mb"], body["container_cpus"])
		}
		if _, ok := body["devcontainer_config"].(string); !ok {
			t.Fatalf("devcontainer_config missing: %v", body)
		}
		if _, ok := body["mise_config"].(string); !ok {
			t.Fatalf("mise_config missing: %v", body)
		}
		if s, _ := body["services_json"].(string); !strings.Contains(s, `"db"`) {
			t.Fatalf("services_json = %v", body["services_json"])
		}
	})
}

func TestCrewCov_UpdatePatch(t *testing.T) {
	t.Parallel()

	t.Run("all scalar fields drift", func(t *testing.T) {
		t.Parallel()
		d := &CrewDocument{
			Metadata: internalapi.Metadata{Name: "New name", Slug: "eng"},
			Spec: CrewSpec{
				Description:  "spec desc",
				Color:        "#000000",
				Icon:         "shield",
				RuntimeImage: "img:2",
			},
		}
		remote := &CrewRemote{
			Name:         "Old name",
			Description:  crewStr("old"),
			Color:        crewStr("#ffffff"),
			Icon:         crewStr("terminal"),
			RuntimeImage: crewStr("img:1"),
		}
		patch, err := d.updatePatch(remote)
		if err != nil {
			t.Fatalf("updatePatch: %v", err)
		}
		want := map[string]any{
			"name": "New name", "description": "spec desc",
			"color": "#000000", "icon": "shield", "runtime_image": "img:2",
		}
		for k, v := range want {
			if patch[k] != v {
				t.Errorf("patch[%q] = %v, want %v", k, patch[k], v)
			}
		}
	})
	t.Run("metadata description wins over spec", func(t *testing.T) {
		t.Parallel()
		d := &CrewDocument{
			Metadata: internalapi.Metadata{Name: "n", Slug: "eng", Description: "meta"},
			Spec:     CrewSpec{Description: "spec"},
		}
		remote := &CrewRemote{Name: "n", Description: crewStr("old")}
		patch, err := d.updatePatch(remote)
		if err != nil {
			t.Fatalf("updatePatch: %v", err)
		}
		if patch["description"] != "meta" {
			t.Fatalf("patch = %v", patch)
		}
	})
	t.Run("devcontainer config and sizing drift", func(t *testing.T) {
		t.Parallel()
		d := &CrewDocument{
			Metadata: internalapi.Metadata{Name: "n", Slug: "eng"},
			Spec: CrewSpec{
				Devcontainer: &Devcontainer{
					Env:      map[string]string{"A": "B"},
					MemoryMB: 4096,
					CPUs:     4,
				},
			},
		}
		remote := &CrewRemote{
			Name:               "n",
			DevcontainerConfig: crewStr(`{"containerEnv":{"A":"OLD"}}`),
			ContainerMemoryMB:  1024,
			ContainerCPUs:      1,
		}
		patch, err := d.updatePatch(remote)
		if err != nil {
			t.Fatalf("updatePatch: %v", err)
		}
		if _, ok := patch["devcontainer_config"]; !ok {
			t.Fatalf("devcontainer_config not patched: %v", patch)
		}
		if patch["container_memory_mb"] != 4096 || patch["container_cpus"] != 4.0 {
			t.Fatalf("sizing patch = %v", patch)
		}
	})
	t.Run("converged sizing emits nothing", func(t *testing.T) {
		t.Parallel()
		d := &CrewDocument{
			Metadata: internalapi.Metadata{Name: "n", Slug: "eng"},
			Spec:     CrewSpec{Devcontainer: &Devcontainer{MemoryMB: 1024, CPUs: 1}},
		}
		remote := &CrewRemote{
			Name:               "n",
			DevcontainerConfig: crewStr(`{"hostRequirements":{"memory":"1024mb","cpus":1}}`),
			ContainerMemoryMB:  1024,
			ContainerCPUs:      1,
		}
		patch, err := d.updatePatch(remote)
		if err != nil {
			t.Fatalf("updatePatch: %v", err)
		}
		if len(patch) != 0 {
			t.Fatalf("want empty patch, got %v", patch)
		}
	})
	t.Run("mise drift", func(t *testing.T) {
		t.Parallel()
		d := &CrewDocument{
			Metadata: internalapi.Metadata{Name: "n", Slug: "eng"},
			Spec:     CrewSpec{Mise: &MiseConfig{Tools: map[string]string{"node": "22"}}},
		}
		remote := &CrewRemote{Name: "n", MiseConfig: crewStr(`{"tools":{"node":"20"}}`)}
		patch, err := d.updatePatch(remote)
		if err != nil {
			t.Fatalf("updatePatch: %v", err)
		}
		if _, ok := patch["mise_config"]; !ok {
			t.Fatalf("mise_config not patched: %v", patch)
		}
	})
	t.Run("declared empty services clears remote", func(t *testing.T) {
		t.Parallel()
		d := &CrewDocument{
			Metadata: internalapi.Metadata{Name: "n", Slug: "eng"},
			Spec:     CrewSpec{Services: []Service{}},
		}
		remote := &CrewRemote{Name: "n", ServicesJSON: crewStr(`[{"name":"db","image":"x"}]`)}
		patch, err := d.updatePatch(remote)
		if err != nil {
			t.Fatalf("updatePatch: %v", err)
		}
		if patch["services_json"] != "[]" {
			t.Fatalf("patch = %v", patch)
		}
	})
	t.Run("declared empty services already clear stays unchanged", func(t *testing.T) {
		t.Parallel()
		d := &CrewDocument{
			Metadata: internalapi.Metadata{Name: "n", Slug: "eng"},
			Spec:     CrewSpec{Services: []Service{}},
		}
		remote := &CrewRemote{Name: "n", ServicesJSON: crewStr(`[]`)}
		patch, err := d.updatePatch(remote)
		if err != nil {
			t.Fatalf("updatePatch: %v", err)
		}
		if len(patch) != 0 {
			t.Fatalf("want empty patch, got %v", patch)
		}
	})
	t.Run("declared services drift", func(t *testing.T) {
		t.Parallel()
		d := &CrewDocument{
			Metadata: internalapi.Metadata{Name: "n", Slug: "eng"},
			Spec:     CrewSpec{Services: []Service{{Name: "db", Image: "postgres:17"}}},
		}
		remote := &CrewRemote{Name: "n", ServicesJSON: crewStr(`[{"name":"db","image":"postgres:16"}]`)}
		patch, err := d.updatePatch(remote)
		if err != nil {
			t.Fatalf("updatePatch: %v", err)
		}
		if s, _ := patch["services_json"].(string); !strings.Contains(s, "postgres:17") {
			t.Fatalf("patch = %v", patch)
		}
	})
	t.Run("nil services leaves remote alone", func(t *testing.T) {
		t.Parallel()
		d := &CrewDocument{Metadata: internalapi.Metadata{Name: "n", Slug: "eng"}}
		remote := &CrewRemote{Name: "n", ServicesJSON: crewStr(`[{"name":"db","image":"x"}]`)}
		patch, err := d.updatePatch(remote)
		if err != nil {
			t.Fatalf("updatePatch: %v", err)
		}
		if len(patch) != 0 {
			t.Fatalf("want empty patch, got %v", patch)
		}
	})
}

// ── Plan exec paths ─────────────────────────────────────────────────

func TestCrewCov_Plan_ExecPaths(t *testing.T) {
	t.Parallel()

	doc := &CrewDocument{
		APIVersion: "crewship/v1",
		Kind:       "Crew",
		Metadata:   internalapi.Metadata{Name: "Eng", Slug: "eng"},
		Spec:       CrewSpec{RuntimeImage: "img:1"},
	}

	t.Run("create post transport error", func(t *testing.T) {
		t.Parallel()
		c := newCovClient(map[string]covRoute{"POST /api/v1/crews": {err: errors.New("down")}})
		items, err := doc.Plan(context.Background(), c, nil)
		if err != nil || len(items) != 1 || items[0].Action != internalapi.ActionCreate {
			t.Fatalf("plan: items=%v err=%v", items, err)
		}
		if execErr := items[0].Exec(context.Background(), c); execErr == nil || !strings.Contains(execErr.Error(), "POST /api/v1/crews") {
			t.Fatalf("exec: got %v", execErr)
		}
	})
	t.Run("create post bad status", func(t *testing.T) {
		t.Parallel()
		c := newCovClient(map[string]covRoute{"POST /api/v1/crews": {status: 409, body: "dup"}})
		items, _ := doc.Plan(context.Background(), c, nil)
		if execErr := items[0].Exec(context.Background(), c); execErr == nil || !strings.Contains(execErr.Error(), "409") {
			t.Fatalf("exec: got %v", execErr)
		}
	})
	t.Run("update patch transport error", func(t *testing.T) {
		t.Parallel()
		remote := &CrewRemote{ID: "c1", Name: "Eng", Slug: "eng", RuntimeImage: crewStr("img:0")}
		c := newCovClient(map[string]covRoute{"PATCH /api/v1/crews/c1": {err: errors.New("down")}})
		items, err := doc.Plan(context.Background(), c, remote)
		if err != nil || len(items) != 1 || items[0].Action != internalapi.ActionUpdate {
			t.Fatalf("plan: items=%v err=%v", items, err)
		}
		if execErr := items[0].Exec(context.Background(), c); execErr == nil || !strings.Contains(execErr.Error(), "PATCH /api/v1/crews/c1") {
			t.Fatalf("exec: got %v", execErr)
		}
	})
	t.Run("update patch ok", func(t *testing.T) {
		t.Parallel()
		remote := &CrewRemote{ID: "c1", Name: "Eng", Slug: "eng", RuntimeImage: crewStr("img:0")}
		c := newCovClient(map[string]covRoute{"PATCH /api/v1/crews/c1": {status: 200, body: "{}"}})
		items, _ := doc.Plan(context.Background(), c, remote)
		if execErr := items[0].Exec(context.Background(), c); execErr != nil {
			t.Fatalf("exec: %v", execErr)
		}
	})
}

// ── listCrewRemotes ─────────────────────────────────────────────────

func TestCrewCov_ListCrewRemotes(t *testing.T) {
	t.Parallel()

	t.Run("transport error", func(t *testing.T) {
		t.Parallel()
		c := newCovClient(map[string]covRoute{"GET /api/v1/crews": {err: errors.New("down")}})
		_, err := listCrewRemotes(context.Background(), c)
		if err == nil || !strings.Contains(err.Error(), "GET /api/v1/crews") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("bad status", func(t *testing.T) {
		t.Parallel()
		c := newCovClient(map[string]covRoute{"GET /api/v1/crews": {status: 500, body: "err"}})
		_, err := listCrewRemotes(context.Background(), c)
		if err == nil || !strings.Contains(err.Error(), "500") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("read failure", func(t *testing.T) {
		t.Parallel()
		c := newCovClient(map[string]covRoute{"GET /api/v1/crews": {badBody: true}})
		_, err := listCrewRemotes(context.Background(), c)
		if err == nil || !strings.Contains(err.Error(), "read /api/v1/crews body") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("empty body", func(t *testing.T) {
		t.Parallel()
		c := newCovClient(map[string]covRoute{"GET /api/v1/crews": {body: ""}})
		rows, err := listCrewRemotes(context.Background(), c)
		if rows != nil || err != nil {
			t.Fatalf("got (%v,%v)", rows, err)
		}
	})
	t.Run("wrapped shape", func(t *testing.T) {
		t.Parallel()
		c := newCovClient(map[string]covRoute{"GET /api/v1/crews": {body: `{"crews":[{"id":"c1","slug":"eng","name":"Eng"}]}`}})
		rows, err := listCrewRemotes(context.Background(), c)
		if err != nil || len(rows) != 1 || rows[0].Slug != "eng" {
			t.Fatalf("got (%v,%v)", rows, err)
		}
	})
	t.Run("invalid both shapes", func(t *testing.T) {
		t.Parallel()
		c := newCovClient(map[string]covRoute{"GET /api/v1/crews": {body: "[1,2]"}})
		_, err := listCrewRemotes(context.Background(), c)
		if err == nil || !strings.Contains(err.Error(), "decode /api/v1/crews") {
			t.Fatalf("got %v", err)
		}
	})
}

// ── parseMiseConfigJSON ─────────────────────────────────────────────

func TestCrewCov_ParseMiseConfigJSON(t *testing.T) {
	t.Parallel()

	t.Run("empty string", func(t *testing.T) {
		t.Parallel()
		mc, err := parseMiseConfigJSON("   ")
		if mc != nil || err != nil {
			t.Fatalf("got (%v,%v)", mc, err)
		}
	})
	t.Run("invalid json", func(t *testing.T) {
		t.Parallel()
		_, err := parseMiseConfigJSON("{nope")
		if err == nil || !strings.Contains(err.Error(), "decode mise_config") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("tools and raw separated", func(t *testing.T) {
		t.Parallel()
		mc, err := parseMiseConfigJSON(`{"tools":{"node":"22","weird":7},"env":{"X":"y"}}`)
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if mc.Tools["node"] != "22" {
			t.Fatalf("tools = %v", mc.Tools)
		}
		if _, ok := mc.Tools["weird"]; ok {
			t.Fatalf("non-string tool value must be dropped: %v", mc.Tools)
		}
		if mc.Raw["env"] == nil {
			t.Fatalf("raw = %v", mc.Raw)
		}
	})
	t.Run("nothing meaningful returns nil", func(t *testing.T) {
		t.Parallel()
		mc, err := parseMiseConfigJSON(`{"tools":{}}`)
		if mc != nil || err != nil {
			t.Fatalf("got (%v,%v)", mc, err)
		}
	})
}

// ── validateCrewServices edge branches ──────────────────────────────

func TestCrewCov_ValidateCrewServices(t *testing.T) {
	t.Parallel()

	if err := validateCrewServices("eng", nil); err != nil {
		t.Fatalf("nil services: %v", err)
	}

	cases := []struct {
		name     string
		services []Service
		wantErr  string
	}{
		{
			name:     "duplicate mount",
			services: []Service{{Name: "db", Image: "x", Volumes: []Volume{{Name: "v1", Mount: "/data"}, {Name: "v2", Mount: "/data"}}}},
			wantErr:  `mounts "/data" more than once`,
		},
		{
			name:     "negative retries",
			services: []Service{{Name: "db", Image: "x", Healthcheck: &Healthcheck{Test: []string{"CMD", "true"}, Retries: -1}}},
			wantErr:  "retries must be non-negative",
		},
		{
			name:     "bad duration",
			services: []Service{{Name: "db", Image: "x", Healthcheck: &Healthcheck{Test: []string{"CMD"}, Interval: "5sec"}}},
			wantErr:  "is not a valid Go duration",
		},
		{
			name:     "healthcheck without test",
			services: []Service{{Name: "db", Image: "x", Healthcheck: &Healthcheck{}}},
			wantErr:  "healthcheck declared without test command",
		},
		{
			name:     "path-looking volume name",
			services: []Service{{Name: "db", Image: "x", Volumes: []Volume{{Name: "./local", Mount: "/data"}}}},
			wantErr:  "looks like a path",
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := validateCrewServices("eng", tc.services)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("want %q, got %v", tc.wantErr, err)
			}
		})
	}

	ok := []Service{{
		Name: "db", Image: "postgres:16",
		Ports:   []string{"5432", "53/udp"},
		Volumes: []Volume{{Name: "data", Mount: "/var/lib/postgresql"}},
		Healthcheck: &Healthcheck{
			Test: []string{"CMD", "pg_isready"}, Interval: "5s", Timeout: "3s", StartPeriod: "10s", Retries: 3,
		},
	}}
	if err := validateCrewServices("eng", ok); err != nil {
		t.Fatalf("valid services rejected: %v", err)
	}
}

func TestCrewCov_Validate_EdgeBranches(t *testing.T) {
	t.Parallel()

	base := func() *CrewDocument {
		return &CrewDocument{
			Metadata: internalapi.Metadata{Name: "Eng", Slug: "eng"},
			Spec:     CrewSpec{RuntimeImage: "img:1"},
		}
	}

	t.Run("negative cpus rejected", func(t *testing.T) {
		t.Parallel()
		d := base()
		d.Spec.Devcontainer = &Devcontainer{CPUs: -1}
		err := d.Validate(internalapi.WorkspaceContext{})
		if err == nil || !strings.Contains(err.Error(), "cpus must be non-negative") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("negative memory rejected", func(t *testing.T) {
		t.Parallel()
		d := base()
		d.Spec.Devcontainer = &Devcontainer{MemoryMB: -1}
		err := d.Validate(internalapi.WorkspaceContext{})
		if err == nil || !strings.Contains(err.Error(), "memory_mb must be non-negative") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("service name not a DNS label", func(t *testing.T) {
		t.Parallel()
		d := base()
		d.Spec.Services = []Service{{Name: "Bad_Name", Image: "x"}}
		err := d.Validate(internalapi.WorkspaceContext{})
		if err == nil || !strings.Contains(err.Error(), "must be a DNS label") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("volume requires name and mount", func(t *testing.T) {
		t.Parallel()
		d := base()
		d.Spec.Services = []Service{{Name: "db", Image: "x", Volumes: []Volume{{Name: "", Mount: "/data"}}}}
		err := d.Validate(internalapi.WorkspaceContext{})
		if err == nil || !strings.Contains(err.Error(), "requires both name and mount") {
			t.Fatalf("got %v", err)
		}
	})
}

// ── marshal-failure chains via unmarshalable Raw values ─────────────

func TestCrewCov_MarshalFailureChains(t *testing.T) {
	t.Parallel()

	// json.Marshal cannot serialise a channel — planting one in the
	// Raw passthrough lets us reach the error returns of the JSON
	// renderers and the createBody / updatePatch / Plan wrappers.
	badRaw := map[string]any{"poison": make(chan int)}

	t.Run("devcontainerJSON marshal error", func(t *testing.T) {
		t.Parallel()
		d := &CrewDocument{Spec: CrewSpec{Devcontainer: &Devcontainer{Raw: badRaw}}}
		_, err := d.devcontainerJSON()
		if err == nil || !strings.Contains(err.Error(), "marshal devcontainer config") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("miseJSON marshal error", func(t *testing.T) {
		t.Parallel()
		d := &CrewDocument{Spec: CrewSpec{Mise: &MiseConfig{Raw: badRaw}}}
		_, err := d.miseJSON()
		if err == nil || !strings.Contains(err.Error(), "marshal mise config") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("createBody devcontainer error", func(t *testing.T) {
		t.Parallel()
		d := &CrewDocument{
			Metadata: internalapi.Metadata{Name: "Eng", Slug: "eng"},
			Spec:     CrewSpec{RuntimeImage: "img:1", Devcontainer: &Devcontainer{Raw: badRaw}},
		}
		if _, err := d.createBody(); err == nil {
			t.Fatal("want error")
		}
	})
	t.Run("createBody mise error", func(t *testing.T) {
		t.Parallel()
		d := &CrewDocument{
			Metadata: internalapi.Metadata{Name: "Eng", Slug: "eng"},
			Spec:     CrewSpec{RuntimeImage: "img:1", Mise: &MiseConfig{Raw: badRaw}},
		}
		if _, err := d.createBody(); err == nil {
			t.Fatal("want error")
		}
	})
	t.Run("updatePatch devcontainer error", func(t *testing.T) {
		t.Parallel()
		d := &CrewDocument{
			Metadata: internalapi.Metadata{Name: "Eng", Slug: "eng"},
			Spec:     CrewSpec{Devcontainer: &Devcontainer{Raw: badRaw}},
		}
		if _, err := d.updatePatch(&CrewRemote{Name: "Eng"}); err == nil {
			t.Fatal("want error")
		}
	})
	t.Run("updatePatch mise error", func(t *testing.T) {
		t.Parallel()
		d := &CrewDocument{
			Metadata: internalapi.Metadata{Name: "Eng", Slug: "eng"},
			Spec:     CrewSpec{Mise: &MiseConfig{Raw: badRaw}},
		}
		if _, err := d.updatePatch(&CrewRemote{Name: "Eng"}); err == nil {
			t.Fatal("want error")
		}
	})
	t.Run("Plan wraps createBody error", func(t *testing.T) {
		t.Parallel()
		d := &CrewDocument{
			Metadata: internalapi.Metadata{Name: "Eng", Slug: "eng"},
			Spec:     CrewSpec{RuntimeImage: "img:1", Devcontainer: &Devcontainer{Raw: badRaw}},
		}
		_, err := d.Plan(context.Background(), newCovClient(nil), nil)
		if err == nil || !strings.Contains(err.Error(), "assemble create body") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("Plan wraps updatePatch error", func(t *testing.T) {
		t.Parallel()
		d := &CrewDocument{
			Metadata: internalapi.Metadata{Name: "Eng", Slug: "eng"},
			Spec:     CrewSpec{Devcontainer: &Devcontainer{Raw: badRaw}},
		}
		_, err := d.Plan(context.Background(), newCovClient(nil), &CrewRemote{ID: "c1", Name: "Eng"})
		if err == nil || !strings.Contains(err.Error(), "build update patch") {
			t.Fatalf("got %v", err)
		}
	})
}

// ── ExportCrews / parseDevcontainerJSON ─────────────────────────────

func TestCrewCov_ExportCrews_ListError(t *testing.T) {
	t.Parallel()

	c := newCovClient(map[string]covRoute{"GET /api/v1/crews": {status: 500, body: "boom"}})
	_, err := ExportCrews(context.Background(), c)
	if err == nil || !strings.Contains(err.Error(), "export crews") {
		t.Fatalf("got %v", err)
	}
}

func TestCrewCov_ParseDevcontainerJSON_Edges(t *testing.T) {
	t.Parallel()

	t.Run("empty string", func(t *testing.T) {
		t.Parallel()
		dc, err := parseDevcontainerJSON("  ")
		if dc != nil || err != nil {
			t.Fatalf("got (%v,%v)", dc, err)
		}
	})
	t.Run("invalid json", func(t *testing.T) {
		t.Parallel()
		_, err := parseDevcontainerJSON("{nope")
		if err == nil || !strings.Contains(err.Error(), "decode devcontainer_config") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("empty object yields nil", func(t *testing.T) {
		t.Parallel()
		dc, err := parseDevcontainerJSON("{}")
		if dc != nil || err != nil {
			t.Fatalf("got (%v,%v)", dc, err)
		}
	})
	t.Run("full round trip", func(t *testing.T) {
		t.Parallel()
		in := `{"image":"img:1","features":{"f":{}},"containerEnv":{"A":"b"},"postCreateCommand":"make","hostRequirements":{"memory":"2048mb","cpus":2},"remoteUser":"agent"}`
		dc, err := parseDevcontainerJSON(in)
		if err != nil || dc == nil {
			t.Fatalf("got (%v,%v)", dc, err)
		}
		if dc.Image != "img:1" || dc.MemoryMB != 2048 || dc.CPUs != 2 || dc.PostCreateCommand != "make" {
			t.Fatalf("typed fields = %+v", dc)
		}
		if dc.Env["A"] != "b" || dc.Raw["remoteUser"] != "agent" {
			t.Fatalf("env/raw = %v / %v", dc.Env, dc.Raw)
		}
	})
}
