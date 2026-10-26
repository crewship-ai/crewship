//go:build linux

package api

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// This child is a FAKE protocol peer, not a Rust/native qualification. It
// exercises the real inherited-FD process transport, cleanup and fault paths.
func TestCommandNativeFixtureChild(t *testing.T) {
	if os.Getenv("C1_FAKE_CHILD") == "" {
		return
	}
	if os.Getenv("C1_SECRET_NOT_FOR_CHILD") != "" {
		os.Exit(19)
	}
	mode := os.Getenv("C1_FAKE_CHILD")
	f := os.NewFile(3, "fixture-private-fd")
	defer f.Close()
	var cp []byte
	var gen, doc string
	var revision uint64
	var staged map[string]any
	for {
		var header [4]byte
		if _, e := io.ReadFull(f, header[:]); e != nil {
			os.Exit(0)
		}
		n := binary.BigEndian.Uint32(header[:])
		if n == 0 || n > commandNativeFrameLimit {
			os.Exit(21)
		}
		raw := make([]byte, n)
		if _, e := io.ReadFull(f, raw); e != nil {
			os.Exit(22)
		}
		var req map[string]json.RawMessage
		if json.Unmarshal(raw, &req) != nil {
			os.Exit(23)
		}
		var kind, id string
		_ = json.Unmarshal(req["kind"], &kind)
		_ = json.Unmarshal(req["request_id"], &id)
		response := map[string]any{"protocol": 1, "request_id": id, "ok": true}
		var value any
		switch kind {
		case "init":
			_ = json.Unmarshal(req["document"], &doc)
			_ = json.Unmarshal(req["generation"], &gen)
			cp = fakeNativeCheckpoint(doc, gen, 0)
			value = fakeNativeMetadata(cp, gen, 0)
		case "stage":
			if mode == "prewire_no_stage" {
				os.Exit(31)
			}
			if mode == "refused" || mode == "illegal_refusal" {
				response["ok"] = false
				response["error"] = "validation_refused"
				response["no_candidate"] = true
				if mode == "illegal_refusal" {
					response["error"] = "engine_failed"
				}
				break
			}
			var input string
			_ = json.Unmarshal(req["input_json"], &input)
			var envelope struct {
				OpID string `json:"op_id"`
			}
			_ = json.Unmarshal([]byte(input), &envelope)
			cp = fakeNativeCheckpoint(doc, gen, revision+1)
			var a commandHostAdmission
			var admission struct {
				Participant string `json:"participant"`
				Author      string `json:"author"`
				Time        string `json:"time"`
			}
			_ = json.Unmarshal(req["admission"], &admission)
			a.Participant = admission.Participant
			a.Author = admission.Author
			a.Time = admission.Time
			receiptRaw, _ := json.Marshal(map[string]any{"state": "host_committed", "op_id": envelope.OpID, "revision": revision + 1, "actor": a.Participant, "author": a.Author, "command_time": a.Time, "fingerprint": commandDigest([]byte(input))})
			receipt := string(receiptRaw)
			staged = map[string]any{"state": "staged", "stage_ref": "opaque-stage", "op_id": envelope.OpID, "document": doc, "generation": gen, "format": commandFormat, "engine_revision": commandEngineRevision, "engine_sha256": os.Getenv("C1_FAKE_ENGINE_SHA"), "base_revision": revision, "revision": revision + 1, "base_digest": stringFromRaw(req["base_digest"]), "digest": commandDigest(cp), "fingerprint": commandDigest([]byte(input)), "checkpoint_json": string(cp), "receipt_json": receipt}
			value = staged
		case "confirm":
			value = map[string]any{"receipt_json": staged["receipt_json"]}
			revision++
		case "reject":
			value = map[string]any{"state": "rejected"}
			staged = nil
		case "projection":
			value = map[string]any{"generation": gen, "revision": revision, "ids": []string{"block-one"}, "native_json": `{"body":[{"type":"paragraph"}]}`}
		default:
			os.Exit(24)
		}
		if response["ok"] == true {
			response["value"] = value
		}
		if kind == "init" {
			switch mode {
			case "slow":
				time.Sleep(time.Second)
			case "partial":
				binary.BigEndian.PutUint32(header[:], 100)
				_, _ = f.Write(header[:])
				_, _ = f.Write([]byte("{"))
				time.Sleep(time.Second)
				os.Exit(0)
			case "oversize":
				binary.BigEndian.PutUint32(header[:], commandNativeFrameLimit+1)
				_, _ = f.Write(header[:])
				time.Sleep(time.Second)
				os.Exit(0)
			case "correlation":
				response["request_id"] = "other-request"
			case "unknown":
				response["unexpected"] = "denied"
			case "missing":
				delete(response, "value")
			case "runtime", "pin", "source", "patch", "catalog", "format", "checkpoint":
				m := value.(map[string]any)
				field := map[string]string{"runtime": "runtime_ref", "pin": "engine_sha256", "source": "engine_revision", "patch": "patch_sha256", "catalog": "catalog", "format": "format", "checkpoint": "checkpoint_json"}[mode]
				m[field] = "wrong"
			}
		}
		if kind == "stage" && response["ok"] == true {
			m := value.(map[string]any)
			switch mode {
			case "stage_extra":
				m["extra"] = true
			case "receipt_identity":
				m["receipt_json"] = `{"state":"host_committed","op_id":"generation-one:one","revision":1,"actor":"foreign","author":"","command_time":"","fingerprint":"wrong"}`
			case "stage_correlation":
				response["request_id"] = "previous-request"
			}
		}
		if kind == "confirm" && mode == "confirm_receipt" {
			value.(map[string]any)["receipt_json"] = `{"wrong":true}`
		}
		if kind == "projection" && mode == "projection_invalid" {
			value.(map[string]any)["native_json"] = "null"
		}
		raw, _ = json.Marshal(response)
		if mode == "duplicate" && kind == "init" {
			raw = append([]byte(`{"protocol":1,`), raw[1:]...)
		}
		binary.BigEndian.PutUint32(header[:], uint32(len(raw)))
		if commandNativeWrite(f, header[:]) != nil || commandNativeWrite(f, raw) != nil {
			os.Exit(0)
		}
	}
}
func stringFromRaw(raw []byte) string { var s string; _ = json.Unmarshal(raw, &s); return s }
func fakeNativeCheckpoint(doc, gen string, revision uint64) []byte {
	raw, _ := json.Marshal(map[string]any{"format": commandFormat, "document": doc, "generation": gen, "revision": revision, "p": map[string]any{"ids": []string{"block-one"}, "native": map[string]any{"body": []any{map[string]any{"type": "paragraph"}}}}, "engine_identity": map[string]any{"revision": commandEngineRevision, "patch_sha256": commandClockPatch, "catalog": "wordcraft-c1-10-v1"}})
	return raw
}
func fakeNativeMetadata(cp []byte, gen string, revision uint64) map[string]any {
	return map[string]any{"runtime_ref": "fake-runtime", "format": commandFormat, "engine_revision": commandEngineRevision, "engine_sha256": os.Getenv("C1_FAKE_ENGINE_SHA"), "patch_sha256": commandClockPatch, "catalog": "wordcraft-c1-10-v1", "checkpoint_json": string(cp), "generation": gen, "revision": revision}
}
func fakeNativeConfig(t *testing.T, mode string) commandNativeConfig {
	t.Helper()
	exe, e := os.Executable()
	if e != nil {
		t.Fatal(e)
	}
	dir := t.TempDir()
	engine := filepath.Join(dir, "fake-engine")
	if e = os.WriteFile(engine, []byte("fake fixture executable"), 0700); e != nil {
		t.Fatal(e)
	}
	hash, e := commandNativeFileDigest(engine)
	if e != nil {
		t.Fatal(e)
	}
	host := filepath.Join(dir, "fake-host")
	script := "#!/bin/sh\nexport C1_FAKE_CHILD='" + mode + "'\nexport C1_FAKE_ENGINE_SHA='" + hash + "'\nexec '" + strings.ReplaceAll(exe, "'", "'\\''") + "' -test.run '^TestCommandNativeFixtureChild$'\n"
	if e = os.WriteFile(host, []byte(script), 0700); e != nil {
		t.Fatal(e)
	}
	return commandNativeConfig{Enabled: true, HostBinary: host, EngineBinary: engine, EngineSHA256: hash, RuntimeRef: "fake-runtime"}
}
func fakeNativePort(t *testing.T, mode string) *commandNativePort {
	t.Helper()
	p, e := startCommandNativePort(context.Background(), fakeNativeConfig(t, mode))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(p.Close)
	return p
}
func awaitNativeDead(t *testing.T, p *commandNativePort) {
	t.Helper()
	select {
	case <-p.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("poisoned child not reaped")
	}
}
func TestCommandNativeFakeProtocolFaultsPoisonWithoutReplay(t *testing.T) {
	for _, mode := range []string{"partial", "oversize", "correlation", "unknown", "missing", "duplicate", "runtime", "pin", "source", "patch", "catalog", "format", "checkpoint"} {
		t.Run(mode, func(t *testing.T) {
			p := fakeNativePort(t, mode)
			ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
			defer cancel()
			if _, e := p.InitialSnapshot(ctx, "doc-one", "generation-one"); e == nil {
				t.Fatal("malformed peer accepted")
			}
			awaitNativeDead(t, p)
			if _, e := p.InitialSnapshot(context.Background(), "doc-two", "generation-two"); e == nil {
				t.Fatal("poisoned port reused")
			}
		})
	}
}
func TestCommandNativeFakeContextTimeoutAndWaitingCancellation(t *testing.T) {
	p := fakeNativePort(t, "slow")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	started := time.Now()
	if _, e := p.InitialSnapshot(ctx, "doc-one", "generation-one"); !errors.Is(e, errCommandUnknown) {
		t.Fatal(e)
	}
	if time.Since(started) > time.Second {
		t.Fatal("caller Open deadline was replaced by30s RPC ceiling")
	}
	awaitNativeDead(t, p)
	p2 := fakeNativePort(t, "normal")
	<-p2.slot
	ctx2, cancel2 := context.WithCancel(context.Background())
	cancel2()
	if _, e := p2.InitialSnapshot(ctx2, "doc-one", "generation-one"); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	p2.slot <- struct{}{}
	if _, e := p2.InitialSnapshot(context.Background(), "doc-one", "generation-one"); e != nil {
		t.Fatal("waiting cancellation killed unrelated operation", e)
	}
}
func TestCommandNativeFakePinDisabledAndEnvironment(t *testing.T) {
	c := fakeNativeConfig(t, "normal")
	c.Enabled = false
	if _, e := startCommandNativePort(context.Background(), c); e == nil {
		t.Fatal("default-off ignored")
	}
	c.Enabled = true
	c.EngineSHA256 = "sha256:" + strings.Repeat("b", 64)
	if _, e := startCommandNativePort(context.Background(), c); e == nil {
		t.Fatal("wrong actual executable pin accepted")
	}
	t.Setenv("C1_SECRET_NOT_FOR_CHILD", "sensitive")
	p := fakeNativePort(t, "normal")
	if _, e := p.InitialSnapshot(context.Background(), "doc-one", "generation-one"); e != nil {
		t.Fatal("env not scrubbed", e)
	}
}
func TestCommandNativeFakePrivateHandlesAndRefusals(t *testing.T) {
	p := fakeNativePort(t, "normal")
	p2 := fakeNativePort(t, "normal")
	base, e := p.InitialSnapshot(context.Background(), "doc-one", "generation-one")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = p2.InitialSnapshot(context.Background(), "doc-one", "generation-one"); e != nil {
		t.Fatal(e)
	}
	raw := []byte(`{"op_id":"generation-one:one"}`)
	c, e := p.Stage(context.Background(), commandHostAdmission{}, base, raw)
	if e != nil {
		t.Fatal(e)
	}
	if e = p2.Confirm(context.Background(), c, commandHostAdmission{}); e == nil {
		t.Fatal("foreign handle accepted")
	}
	forged := c
	forged.Revision++
	if e = p.Confirm(context.Background(), forged, commandHostAdmission{}); e == nil {
		t.Fatal("changed handle binding accepted")
	}
	if _, e = p.Projection(context.Background()); e != nil {
		t.Fatal("local handle refusal touched stream", e)
	}
	if _, e = p2.Projection(context.Background()); e != nil {
		t.Fatal("foreign handle touched second stream", e)
	}
	if e = p.Confirm(context.Background(), c, commandHostAdmission{}); e != nil {
		t.Fatal(e)
	}
	for _, mode := range []string{"refused", "illegal_refusal"} {
		t.Run(mode, func(t *testing.T) {
			q := fakeNativePort(t, mode)
			b, e := q.InitialSnapshot(context.Background(), "doc-one", "generation-one")
			if e != nil {
				t.Fatal(e)
			}
			_, e = q.Stage(context.Background(), commandHostAdmission{}, b, raw)
			if mode == "refused" {
				if !errors.Is(e, errCommandRefused) {
					t.Fatal(e)
				}
				if _, e = q.Projection(context.Background()); e != nil {
					t.Fatal("clean refusal poisoned stream", e)
				}
			} else {
				if errors.Is(e, errCommandRefused) || e == nil {
					t.Fatal("false pure refusal", e)
				}
				awaitNativeDead(t, q)
			}
		})
	}
}
func TestCommandNativeFakeStrictDecoder(t *testing.T) {
	for _, raw := range []string{`{"name":"ok","name":"duplicate"}`, `{"Name":"casefold"}`, `{"name":"ok","extra":1}`, `{"name":null}`, `null`} {
		var v struct {
			Name string `json:"name"`
		}
		if commandNativeDecode([]byte(raw), &v, "name") == nil {
			t.Fatal("non-closed object accepted", raw)
		}
	}
	a := fakeNativeCheckpoint("doc", "gen", 0)
	if commandNativeCheckpoint(a, "doc", "gen", 0) != nil {
		t.Fatal("positive header rejected")
	}
	if commandNativeCheckpoint(bytes.ReplaceAll(a, []byte(`"revision":0`), []byte(`"revision":1`)), "doc", "gen", 0) == nil {
		t.Fatal("wrong native revision")
	}
}

func TestCommandNativeFakeStageAndACKProtocolPoison(t *testing.T) {
	for _, mode := range []string{"stage_extra", "receipt_identity", "stage_correlation", "confirm_receipt", "projection_invalid"} {
		t.Run(mode, func(t *testing.T) {
			p := fakeNativePort(t, mode)
			base, e := p.InitialSnapshot(context.Background(), "doc-one", "generation-one")
			if e != nil {
				t.Fatal(e)
			}
			if mode == "projection_invalid" {
				if _, e = p.Projection(context.Background()); e == nil {
					t.Fatal("invalid projection accepted")
				}
			} else {
				c, stageErr := p.Stage(context.Background(), commandHostAdmission{}, base, []byte(`{"op_id":"generation-one:one"}`))
				if mode == "confirm_receipt" {
					if stageErr != nil {
						t.Fatal(stageErr)
					}
					if e = p.Confirm(context.Background(), c, commandHostAdmission{}); e == nil {
						t.Fatal("changed ACK accepted")
					}
				} else if stageErr == nil {
					t.Fatal("invalid stage accepted")
				}
			}
			awaitNativeDead(t, p)
		})
	}
}
func TestCommandNativeFakeBindingCannotSwitchDocument(t *testing.T) {
	p := fakeNativePort(t, "normal")
	if _, e := p.InitialSnapshot(context.Background(), "doc-one", "generation-one"); e != nil {
		t.Fatal(e)
	}
	if e := p.RestoreConfirmed(context.Background(), commandSQLSnapshot{Checkpoint: fakeNativeCheckpoint("doc-two", "generation-two", 0)}); e == nil {
		t.Fatal("restore switched document")
	}
	if _, e := p.Projection(context.Background()); e != nil {
		t.Fatal("wrong-document refusal touched transport", e)
	}
}

func TestCommandNativeFakeStagePrewireValidationPreservesExactInput(t *testing.T) {
	p := fakeNativePort(t, "prewire_no_stage")
	base, e := p.InitialSnapshot(context.Background(), "doc-one", "generation-one")
	if e != nil {
		t.Fatal(e)
	}
	for _, raw := range [][]byte{nil, []byte("{"), []byte(`{"op_id":"one","op_id":"two"}`), {0xff}} {
		if _, e = p.Stage(context.Background(), commandHostAdmission{}, base, raw); !errors.Is(e, errCommandStore) {
			t.Fatal("bad input not refused locally", e)
		}
		if _, e = p.Projection(context.Background()); e != nil {
			t.Fatal("invalid input reached child or poisoned channel", e)
		}
	}
	// An expired shared Open context cannot obtain a fresh30s restore budget.
	expired, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Millisecond))
	defer cancel()
	if e = p.RestoreConfirmed(expired, base); !errors.Is(e, context.DeadlineExceeded) {
		t.Fatal("restore ignored exhausted Open context", e)
	}
	if _, e = p.Projection(context.Background()); e != nil {
		t.Fatal("expired context sent restore", e)
	}
	q := fakeNativePort(t, "normal")
	base, e = q.InitialSnapshot(context.Background(), "doc-one", "generation-one")
	if e != nil {
		t.Fatal(e)
	}
	raw := []byte(`{ "op_id" : "generation-one:one" }`)
	candidate, e := q.Stage(context.Background(), commandHostAdmission{}, base, raw)
	if e != nil || candidate.Fingerprint != commandDigest(raw) {
		t.Fatal("port rewrote original input bytes", e)
	}
}
