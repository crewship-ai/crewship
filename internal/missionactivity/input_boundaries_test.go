package missionactivity

import (
	"context"
	"errors"
	"testing"

	"github.com/crewship-ai/crewship/internal/testutil"
)

func TestRejectedActivityDoesNotAllocateSequence(t *testing.T) {
	db := testutil.MigratedSQLDB(t)
	seedMission(t, db, "ws", "crew", "agent", "mission")
	valid := Entry{ID: "event", MissionID: "mission", ActorType: "agent", ActorID: "agent", Action: "status_changed"}
	for _, missing := range []string{"id", "mission", "actor", "action"} {
		t.Run(missing, func(t *testing.T) {
			input := valid
			switch missing {
			case "id":
				input.ID = ""
			case "mission":
				input.MissionID = ""
			case "actor":
				input.ActorType = ""
			case "action":
				input.Action = ""
			}
			if out, err := Emit(t.Context(), db, input); err == nil || out.Seq != 0 {
				t.Fatalf("invalid activity emitted: %#v, %v", out, err)
			}
			tx, err := db.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			if out, err := EmitTx(t.Context(), tx, input); err == nil || out.Seq != 0 {
				t.Fatalf("invalid transactional activity emitted: %#v, %v", out, err)
			}
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if out, err := Emit(ctx, db, valid); !errors.Is(err, context.Canceled) || out.Seq != 0 {
		t.Fatalf("canceled activity emitted: %#v, %v", out, err)
	}
	out, err := Emit(t.Context(), db, valid)
	if err != nil || out.Seq != 1 {
		t.Fatalf("rejected calls consumed sequence: %#v, %v", out, err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM mission_activity WHERE mission_id='mission'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("unexpected events: %d, %v", count, err)
	}
}
