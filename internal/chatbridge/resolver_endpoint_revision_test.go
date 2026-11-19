package chatbridge

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/orchestrator"
)

// #5542: the chat path is how ordinary runs resolve credentials. The
// ENDPOINT_URL proxy projection is admitted only with the revision of the
// exact Vault value, so dropping source_revision here makes every chat run
// through an authenticated endpoint fail with 403 at the host.
func TestResolve_CarriesEndpointSourceRevisionThroughToOrchestratorCredentials(t *testing.T) {
	const body = `{
		"agent_id": "agt_a",
		"agent_slug": "alpha",
		"credentials": [
			{"id": "ep", "env_var": "", "value": "synthetic", "type": "API_KEY", "provider": "OPENAI_COMPAT", "base_url": "https://fixture.example/v1", "source_revision": "rev-1"},
			{"id": "key", "env_var": "OPENAI_API_KEY", "value": "sk", "type": "API_KEY", "provider": "OPENAI"}
		]
	}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	r := NewIPCResolver(srv.URL, "test-internal-token", slog.New(slog.NewTextHandler(discardWriter{}, nil)))
	info, err := r.ResolveAgent(context.Background(), "agt_a", "")
	if err != nil {
		t.Fatalf("ResolveAgent: %v", err)
	}
	if len(info.Credentials) != 2 || info.Credentials[0].SourceRevision != "rev-1" {
		t.Fatalf("source_revision dropped between the API and the orchestrator: %+v", info.Credentials)
	}
	if info.Credentials[1].SourceRevision != "" {
		t.Fatalf("source_revision invented: %+v", info.Credentials[1])
	}
}

// Every field the orchestrator credential carries over JSON must be decodable
// from the chat resolve response, or it silently disappears on the chat path
// while the other delivery paths keep it (as source_revision did).
func TestCredentialResponseCoversOrchestratorCredentialFields(t *testing.T) {
	tags := func(v any) map[string]bool {
		out := map[string]bool{}
		rt := reflect.TypeOf(v)
		for i := 0; i < rt.NumField(); i++ {
			f := rt.Field(i)
			if !f.IsExported() {
				continue
			}
			name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
			if name == "" {
				name = f.Name // encoding/json still encodes untagged fields
			}
			if name != "-" {
				out[name] = true
			}
		}
		return out
	}
	have := tags(credentialResponse{})
	for name := range tags(orchestrator.Credential{}) {
		if !have[name] {
			t.Errorf("orchestrator.Credential json field %q is not decoded by the chat resolver", name)
		}
	}
}

// Decoding is not enough: the resolver copies fields one by one, so a field can
// decode and still be dropped in the mapping. Fill every field of an
// orchestrator credential, send it as the API would, and require it back intact.
func TestResolve_RoundTripsEveryOrchestratorCredentialField(t *testing.T) {
	var want orchestrator.Credential
	fill(t, reflect.ValueOf(&want).Elem(), "f")
	raw, err := json.Marshal(map[string]any{"agent_id": "agt_a", "agent_slug": "alpha", "credentials": []orchestrator.Credential{want}})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(raw)
	}))
	defer srv.Close()
	r := NewIPCResolver(srv.URL, "test-internal-token", slog.New(slog.NewTextHandler(discardWriter{}, nil)))
	info, err := r.ResolveAgent(context.Background(), "agt_a", "")
	if err != nil {
		t.Fatalf("ResolveAgent: %v", err)
	}
	if len(info.Credentials) != 1 {
		t.Fatalf("resolved %d credentials", len(info.Credentials))
	}
	got := reflect.ValueOf(info.Credentials[0])
	wv := reflect.ValueOf(want)
	for i := 0; i < wv.NumField(); i++ {
		if !reflect.DeepEqual(got.Field(i).Interface(), wv.Field(i).Interface()) {
			t.Errorf("chat resolve dropped or changed orchestrator.Credential.%s: got %v want %v", wv.Type().Field(i).Name, got.Field(i).Interface(), wv.Field(i).Interface())
		}
	}
}

// fill sets every exported field to a distinct non-zero value.
func fill(t *testing.T, v reflect.Value, seed string) {
	t.Helper()
	switch v.Kind() {
	case reflect.String:
		v.SetString(seed)
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Int, reflect.Int64, reflect.Int32:
		v.SetInt(int64(len(seed)))
	case reflect.Map:
		m := reflect.MakeMap(v.Type())
		k, e := reflect.New(v.Type().Key()).Elem(), reflect.New(v.Type().Elem()).Elem()
		fill(t, k, seed+"k")
		fill(t, e, seed+"v")
		m.SetMapIndex(k, e)
		v.Set(m)
	case reflect.Slice:
		s := reflect.MakeSlice(v.Type(), 1, 1)
		fill(t, s.Index(0), seed+"0")
		v.Set(s)
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			if v.Type().Field(i).IsExported() {
				fill(t, v.Field(i), fmt.Sprintf("%s.%s", seed, v.Type().Field(i).Name))
			}
		}
	default:
		t.Fatalf("fill: unsupported kind %s; extend the round-trip test", v.Kind())
	}
}
