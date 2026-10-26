package api

import (
	"context"
	"encoding/json"
	"testing"
)

type commandProjectionFixture struct {
	commandProductFixture
	projection commandProductProjection
}

func (p *commandProjectionFixture) Projection(context.Context) (commandProductProjection, error) {
	return p.projection, nil
}

func TestCommandProjectionRequiresConfirmedSQLAndCurrentReader(t *testing.T) {
	for _, scenario := range []string{"confirmed", "engine-ahead", "engine-behind", "revoked", "missing-send"} {
		t.Run(scenario, func(t *testing.T) {
			f, manager, request := commandProductTestManager(t)
			mustExec(t, f.r.db, `DELETE FROM mooring_collab_documents WHERE page_id='admission-page'`)
			port := &commandProjectionFixture{commandProductFixture: commandProductFixture{commandSQLFixturePort: commandSQLFixturePort{done: make(chan struct{})}}, projection: commandProductProjection{Generation: "generation", IDs: []string{"block"}, NativeJSON: `{"body":[]}`}}
			factory, err := newCommandProductFactory(true, manager, func(_ context.Context, runtime string) (commandProductPort, error) {
				port.ref = runtime
				return port, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(factory.Close)
			document, err := factory.Open(request, true)
			if err != nil {
				t.Fatal(err)
			}
			original, err := document.store.Admit(request.Context(), false)
			if err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "engine-ahead":
				port.projection.Revision = 1
			case "engine-behind":
				mustExec(t, f.r.db, `UPDATE mooring_collab_documents SET revision='1' WHERE page_id='admission-page'`)
			case "revoked":
				mustExec(t, f.r.db, `UPDATE users SET authority_revision=authority_revision+1 WHERE id=?`, original.source.UserID)
			}
			sends := 0
			var send func(context.Context, []byte) error
			if scenario != "missing-send" {
				send = func(_ context.Context, raw []byte) error {
					sends++
					var value map[string]json.RawMessage
					if err := json.Unmarshal(raw, &value); err != nil || len(value) != 4 || value["native_json"] == nil || value["revision"] == nil || value["generation"] == nil || value["ids"] == nil {
						t.Fatal("projection leaked checkpoint/history or omitted its revision")
					}
					return nil
				}
			}
			err = document.DeliverProjection(request, original, send)
			if scenario == "confirmed" {
				if err != nil || sends != 1 {
					t.Fatal("confirmed projection not delivered", err, sends)
				}
			} else if err == nil || sends != 0 {
				t.Fatal("unqualified content delivered", err, sends)
			}
		})
	}
}
