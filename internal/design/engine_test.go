package design

import (
	"context"
	"encoding/json"
	"testing"
)

func TestEngineOfflineOperationsAndStandardSync(t *testing.T) {
	a, err := Run(context.Background(), Request{Operations: json.RawMessage(`[{"op":"upsert","node":{"id":"cart","type":"frame","name":"Cart","x":10,"y":20,"width":300,"height":600,"fill":"#ffffff"}}]`)})
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Nodes) != 1 {
		t.Fatalf("nodes %s", a.Nodes)
	}
	b, err := Run(context.Background(), Request{Frame: a.Frame})
	if err != nil {
		t.Fatal(err)
	}
	if string(a.Nodes[0]) != string(b.Nodes[0]) {
		t.Fatalf("sync did not recover nodes")
	}
	c, err := Run(context.Background(), Request{State: b.State, Operations: json.RawMessage(`[{"op":"upsert","node":{"id":"cart","fill":"#ff0000"}}]`)})
	if err != nil {
		t.Fatal(err)
	}
	var n map[string]any
	_ = json.Unmarshal(c.Nodes[0], &n)
	if n["fill"] != "#ff0000" || n["name"] != "Cart" {
		t.Fatalf("partial operation lost fields: %s", c.Nodes[0])
	}
}
func TestEngineRejectsInvalidYjsUpdate(t *testing.T) {
	if _, err := Run(context.Background(), Request{Frame: []byte{0, 2, 1, 255}}); err == nil {
		t.Fatal("malformed update accepted")
	}
	if _, err := Run(context.Background(), Request{Operations: json.RawMessage(`[{"op":"upsert","node":{"id":"x","type":"script"}}]`)}); err == nil {
		t.Fatal("invalid node accepted")
	}
}
func TestEngineRestoreRetainsLiveDocument(t *testing.T) {
	first, e := Run(context.Background(), Request{Operations: json.RawMessage(`[{"op":"upsert","node":{"id":"one","type":"text","x":0,"y":0,"width":100,"height":30,"text":"first"}}]`)})
	if e != nil {
		t.Fatal(e)
	}
	changed, e := Run(context.Background(), Request{State: first.State, Operations: json.RawMessage(`[{"op":"upsert","node":{"id":"one","text":"second"}}]`)})
	if e != nil {
		t.Fatal(e)
	}
	restored, e := Run(context.Background(), Request{State: changed.State, Replace: first.State})
	if e != nil {
		t.Fatal(e)
	}
	client, e := Run(context.Background(), Request{State: changed.State, Frame: restored.Frame})
	if e != nil {
		t.Fatal(e)
	}
	var n map[string]any
	_ = json.Unmarshal(client.Nodes[0], &n)
	if n["text"] != "first" {
		t.Fatalf("restore did not reach live client: %s", client.Nodes[0])
	}
}

func TestEngineReconnectUploadsOfflineUpdates(t *testing.T) {
	base, err := Run(context.Background(), Request{})
	if err != nil {
		t.Fatal(err)
	}
	offline, err := Run(context.Background(), Request{State: base.State, Operations: json.RawMessage(`[{"op":"upsert","node":{"id":"offline","type":"frame","x":0,"y":0,"width":320,"height":640}}]`)})
	if err != nil {
		t.Fatal(err)
	}
	response, err := Run(context.Background(), Request{State: offline.State, Frame: base.Hello})
	if err != nil {
		t.Fatal(err)
	}
	server, err := Run(context.Background(), Request{State: base.State, Frame: response.Response})
	if err != nil {
		t.Fatal(err)
	}
	if len(server.Nodes) != 1 {
		t.Fatalf("offline update did not reach server: %s", server.Nodes)
	}
}

func TestEngineMergesIndependentClientsWithoutLosingNodes(t *testing.T) {
	a, err := Run(context.Background(), Request{Operations: json.RawMessage(`[{"op":"upsert","node":{"id":"a","type":"frame","x":0,"y":0,"width":320,"height":640}}]`)})
	if err != nil {
		t.Fatal(err)
	}
	b, err := Run(context.Background(), Request{Operations: json.RawMessage(`[{"op":"upsert","node":{"id":"b","type":"text","x":0,"y":0,"width":100,"height":30,"text":"Concurrent"}}]`)})
	if err != nil {
		t.Fatal(err)
	}
	ab, err := Run(context.Background(), Request{State: a.State, Frame: b.Frame})
	if err != nil {
		t.Fatal(err)
	}
	ba, err := Run(context.Background(), Request{State: b.State, Frame: a.Frame})
	if err != nil {
		t.Fatal(err)
	}
	if len(ab.Nodes) != 2 || len(ba.Nodes) != 2 {
		t.Fatalf("concurrent nodes lost: %s / %s", ab.Nodes, ba.Nodes)
	}
	again, err := Run(context.Background(), Request{State: ab.State, Frame: b.Frame})
	if err != nil {
		t.Fatal(err)
	}
	if len(again.Nodes) != 2 || len(again.Order) != 2 {
		t.Fatalf("retry duplicated nodes/order: %s / %s", again.Nodes, again.Order)
	}
}
