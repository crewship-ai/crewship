package kinds

// Coverage-focused tests for label.go: decodeLabelList shape
// tolerance, execAndDiscard verb dispatch + error paths, ExportLabels
// failure branches, and the DeletePlanItem Exec.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/manifest/internalapi"
)

func TestLabelCov_DecodeLabelList(t *testing.T) {
	t.Parallel()

	t.Run("non-2xx with preview", func(t *testing.T) {
		resp := &internalapi.Response{StatusCode: 500, Body: strings.NewReader("server hiccup")}
		_, err := decodeLabelList(resp)
		if err == nil || !strings.Contains(err.Error(), "HTTP 500") || !strings.Contains(err.Error(), "server hiccup") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("non-2xx nil body", func(t *testing.T) {
		resp := &internalapi.Response{StatusCode: 503}
		if _, err := decodeLabelList(resp); err == nil || !strings.Contains(err.Error(), "HTTP 503") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("2xx nil body → nil rows", func(t *testing.T) {
		rows, err := decodeLabelList(&internalapi.Response{StatusCode: 200})
		if err != nil || rows != nil {
			t.Fatalf("rows=%v err=%v", rows, err)
		}
	})
	t.Run("body read failure", func(t *testing.T) {
		resp := &internalapi.Response{StatusCode: 200, Body: covErrReader{}}
		if _, err := decodeLabelList(resp); err == nil || !strings.Contains(err.Error(), "read labels body") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("empty body → nil rows", func(t *testing.T) {
		rows, err := decodeLabelList(&internalapi.Response{StatusCode: 200, Body: strings.NewReader("")})
		if err != nil || rows != nil {
			t.Fatalf("rows=%v err=%v", rows, err)
		}
	})
	t.Run("flat array", func(t *testing.T) {
		resp := &internalapi.Response{StatusCode: 200, Body: strings.NewReader(`[{"id":"l1","name":"bug","color":"#ff0000"}]`)}
		rows, err := decodeLabelList(resp)
		if err != nil || len(rows) != 1 || rows[0].Name != "bug" {
			t.Fatalf("rows=%v err=%v", rows, err)
		}
	})
	t.Run("wrapped object", func(t *testing.T) {
		resp := &internalapi.Response{StatusCode: 200, Body: strings.NewReader(`{"labels":[{"id":"l1","name":"bug","color":"#ff0000"}]}`)}
		rows, err := decodeLabelList(resp)
		if err != nil || len(rows) != 1 || rows[0].Color != "#ff0000" {
			t.Fatalf("rows=%v err=%v", rows, err)
		}
	})
	t.Run("unrecognized shape", func(t *testing.T) {
		resp := &internalapi.Response{StatusCode: 200, Body: strings.NewReader(`"just a string"`)}
		if _, err := decodeLabelList(resp); err == nil || !strings.Contains(err.Error(), "unrecognized response shape") {
			t.Fatalf("got %v", err)
		}
	})
}

func TestLabelCov_ExecAndDiscard(t *testing.T) {
	t.Parallel()
	c := newCovClient(map[string]covRoute{
		"POST /x":   {status: 201, body: `{}`},
		"PATCH /x":  {status: 200, body: `{}`},
		"PUT /x":    {status: 200, body: `{}`},
		"DELETE /x": {status: 204},
		"POST /bad": {status: 409, body: "conflict"},
		"POST /err": {err: errors.New("down")},
		"POST /nil": {nilResp: true},
	})

	for _, m := range []string{"POST", "PATCH", "PUT", "DELETE"} {
		if err := execAndDiscard(context.Background(), c, m, "/x", nil); err != nil {
			t.Errorf("%s /x: %v", m, err)
		}
	}
	if err := execAndDiscard(context.Background(), c, "TRACE", "/x", nil); err == nil || !strings.Contains(err.Error(), "unsupported HTTP method") {
		t.Errorf("TRACE: %v", err)
	}
	if err := execAndDiscard(context.Background(), c, "POST", "/bad", nil); err == nil || !strings.Contains(err.Error(), "HTTP 409") || !strings.Contains(err.Error(), "conflict") {
		t.Errorf("409: %v", err)
	}
	if err := execAndDiscard(context.Background(), c, "POST", "/err", nil); err == nil || !strings.Contains(err.Error(), "down") {
		t.Errorf("transport: %v", err)
	}
	if err := execAndDiscard(context.Background(), c, "POST", "/nil", nil); err == nil || !strings.Contains(err.Error(), "nil response") {
		t.Errorf("nil resp: %v", err)
	}
}

func TestLabelCov_ExportLabels(t *testing.T) {
	t.Parallel()
	path := "/api/v1/labels"

	t.Run("transport error", func(t *testing.T) {
		c := newCovClient(map[string]covRoute{"GET " + path: {err: errors.New("down")}})
		if _, err := ExportLabels(context.Background(), c); err == nil || !strings.Contains(err.Error(), "list labels") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("nil response", func(t *testing.T) {
		c := newCovClient(map[string]covRoute{"GET " + path: {nilResp: true}})
		if _, err := ExportLabels(context.Background(), c); err == nil || !strings.Contains(err.Error(), "nil response") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("decode error propagates", func(t *testing.T) {
		c := newCovClient(map[string]covRoute{"GET " + path: {status: 500, body: "x"}})
		if _, err := ExportLabels(context.Background(), c); err == nil {
			t.Fatal("want error")
		}
	})
	t.Run("docs preserve slug == name", func(t *testing.T) {
		c := newCovClient(map[string]covRoute{"GET " + path: {body: `[{"id":"l1","name":"bug","color":"#ff0000","description":"red alert"}]`}})
		docs, err := ExportLabels(context.Background(), c)
		if err != nil || len(docs) != 1 {
			t.Fatalf("docs=%v err=%v", docs, err)
		}
		d := docs[0]
		if d.Metadata.Name != "bug" || d.Metadata.Slug != "bug" {
			t.Errorf("metadata = %+v", d.Metadata)
		}
		if d.Spec.Color != "#ff0000" || d.Spec.Description != "red alert" {
			t.Errorf("spec = %+v", d.Spec)
		}
	})
}

func TestLabelCov_DeletePlanItemExec(t *testing.T) {
	t.Parallel()
	item := DeletePlanItem(LabelRemote{ID: "l1", Name: "bug"})
	if item.Action != internalapi.ActionDelete || item.Slug != "bug" {
		t.Fatalf("item = %+v", item)
	}
	c := newCovClient(map[string]covRoute{"DELETE /api/v1/labels/l1": {status: 204}})
	if err := item.Exec(context.Background(), c); err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if !c.sawCall("DELETE /api/v1/labels/l1") {
		t.Errorf("calls = %v", c.calls)
	}
}
