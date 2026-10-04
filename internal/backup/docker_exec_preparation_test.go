package backup

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/moby/moby/client"
)

func TestExecPreparationRefusalAndFenceOrder(t *testing.T) {
	for _, operation := range []string{"exec", "restore"} {
		for _, boundary := range []string{"prepare", "before-start", "create"} {
			t.Run(operation+"/"+boundary, func(t *testing.T) {
				denied := errors.New("boot changed")
				var events []string
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					events = append(events, "create")
					if !strings.HasSuffix(r.URL.Path, "/containers/owned/exec") {
						t.Errorf("unexpected Docker operation %s %s", r.Method, r.URL.Path)
					}
					var cfg client.ExecCreateOptions
					if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
						t.Fatal(err)
					}
					if cfg.User != "1001:1001" || !reflect.DeepEqual(cfg.Env, []string{"TOKEN=synthetic"}) || len(cfg.Cmd) < 4 || !reflect.DeepEqual(cfg.Cmd[:3], []string{"/trusted-gate", "--exec", "boot-nonce"}) {
						t.Errorf("prepared exec changed: %+v", cfg)
					}
					if operation == "restore" && cfg.Cmd[3] != "tar" {
						t.Errorf("restore did not enter gate: %v", cfg.Cmd)
					}
					if boundary == "create" {
						w.WriteHeader(http.StatusConflict)
						w.Write([]byte(`{"message":"boot changed"}`))
						return
					}
					w.WriteHeader(http.StatusCreated)
					w.Write([]byte(`{"Id":"exec-owned"}`))
				}))
				defer server.Close()
				cli, err := client.New(client.WithHost(server.URL), client.WithHTTPClient(server.Client()), client.WithAPIVersion("1.52"))
				if err != nil {
					t.Fatal(err)
				}
				defer cli.Close()
				ops := &MobyDockerOps{Client: cli, Guard: func(context.Context, string) (func(context.Context) error, func(context.Context) error, error) {
					t.Fatal("duplicate guard invocation")
					return nil, nil, nil
				}, Prepare: func(_ context.Context, id string, cmd, env []string) ([]string, []string, func(context.Context) error, func(context.Context) error, error) {
					events = append(events, "prepare")
					if id != "owned" {
						t.Errorf("wrong scope %s", id)
					}
					if boundary == "prepare" {
						return nil, nil, nil, nil, denied
					}
					return append([]string{"/trusted-gate", "--exec", "boot-nonce"}, cmd...), []string{"TOKEN=synthetic"}, func(context.Context) error { events = append(events, "before-start"); return denied }, nil, nil
				}}
				if operation == "exec" {
					_, _, err = ops.ExecAs(t.Context(), "owned", "1001:1001", []string{"sync"})
				} else {
					err = ops.CopyToPath(t.Context(), "owned", ExtractSpec{Dest: "/crew", User: "1001:1001"}, strings.NewReader("archive"))
				}
				if err == nil {
					t.Fatal("refused exec admitted")
				}
				want := []string{"prepare"}
				if boundary != "prepare" {
					want = append(want, "create")
				}
				if boundary == "before-start" {
					want = append(want, "before-start")
				}
				if !reflect.DeepEqual(events, want) {
					t.Fatalf("events=%v want %v", events, want)
				}
			})
		}
	}
}

func TestExecPreparationLegacyRetainsGuardAndCommand(t *testing.T) {
	calls := 0
	ops := &MobyDockerOps{Guard: func(context.Context, string) (func(context.Context) error, func(context.Context) error, error) {
		calls++
		return nil, nil, nil
	}}
	cmd := []string{"sync"}
	got, env, before, after, err := ops.prepare(t.Context(), "legacy", cmd)
	if err != nil || calls != 1 || !reflect.DeepEqual(got, cmd) || env != nil || before(t.Context()) != nil || after(t.Context()) != nil {
		t.Fatalf("legacy changed: %v %v %v calls=%d", got, env, err, calls)
	}
	ops.Prepare = func(context.Context, string, []string, []string) ([]string, []string, func(context.Context) error, func(context.Context) error, error) {
		return cmd, nil, nil, nil, nil
	}
	_, _, before, after, err = ops.prepare(t.Context(), "legacy", cmd)
	if err != nil || calls != 1 || before(t.Context()) != nil || after(t.Context()) != nil {
		t.Fatal("nil prepare callbacks or duplicate guard")
	}
}

func TestExecPreparationPreservesChecksAroundActualStart(t *testing.T) {
	for _, operation := range []string{"exec", "restore"} {
		for _, failAfter := range []bool{false, true} {
			name := operation + "/success"
			if failAfter {
				name = operation + "/after-refusal"
			}
			t.Run(name, func(t *testing.T) {
				d := newFakeDaemon()
				d.execReadStdin = operation == "restore"
				ops := newMobyOps(t, d)
				var events []string
				refused := errors.New("restart observed after attach")
				ops.Prepare = func(_ context.Context, _ string, cmd, _ []string) ([]string, []string, func(context.Context) error, func(context.Context) error, error) {
					events = append(events, "prepare")
					return append([]string{"gate", "nonce"}, cmd...), nil, func(context.Context) error { events = append(events, "before"); return nil }, func(context.Context) error {
						events = append(events, "after")
						if failAfter {
							return refused
						}
						return nil
					}, nil
				}
				var err error
				if operation == "exec" {
					_, _, err = ops.ExecAs(t.Context(), "owned", "1001:1001", []string{"sync"})
				} else {
					err = ops.CopyToPath(t.Context(), "owned", ExtractSpec{Dest: "/crew", User: "1001:1001"}, strings.NewReader("synthetic archive"))
				}
				if failAfter {
					if !errors.Is(err, refused) {
						t.Fatalf("after-start refusal lost: %v", err)
					}
				} else if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(events, []string{"prepare", "before", "after"}) {
					t.Fatalf("check order=%v", events)
				}
				d.mu.Lock()
				defer d.mu.Unlock()
				if len(d.execCmd) < 3 || !reflect.DeepEqual(d.execCmd[:2], []string{"gate", "nonce"}) || d.execUser != "1001:1001" {
					t.Fatalf("started unwrapped command/user: %v %s", d.execCmd, d.execUser)
				}
			})
		}
	}
}
