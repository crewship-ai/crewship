package artifacts

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
)

func TestDataStoreAtomicCASProvenanceAndCodeVersions(t *testing.T) {
	v, k, a, db := versionFixture(t, VersionOptions{})
	if _, err := v.Apply(t.Context(), k, versionRequest(0, "Dashboard"), a); err != nil {
		t.Fatal(err)
	}
	s := NewDataStore(db.DB, DataOptions{Enabled: true})
	write := func(id string, version int64, n string) DataWrite {
		return DataWrite{ID: id, IfVersion: version, Value: json.RawMessage(`{"n":` + n + `}`)}
	}
	docs, err := s.Batch(t.Context(), k, "sales", []DataWrite{write("a", 0, "9007199254740993"), write("b", 0, "2")}, a)
	if err != nil || len(docs) != 2 {
		t.Fatalf("initial batch: %+v %v", docs, err)
	}
	if docs[0].Version != 1 || docs[0].UpdatedBy != "owner" || docs[0].UpdatedAt == "" || string(docs[0].Value) != `{"n":9007199254740993}` {
		t.Fatalf("provenance or exact JSON: %+v", docs[0])
	}
	_, err = s.Batch(t.Context(), k, "sales", []DataWrite{write("a", 1, "10"), write("b", 0, "20")}, a)
	if !errors.Is(err, ErrDataConflict) {
		t.Fatalf("stale second document: %v", err)
	}
	docs, err = s.Read(t.Context(), k, "sales", a)
	if err != nil || len(docs) != 2 || docs[0].Version != 1 || string(docs[0].Value) != `{"n":9007199254740993}` {
		t.Fatalf("partial batch committed: %+v %v", docs, err)
	}
	collections, err := s.Collections(t.Context(), k, a)
	if err != nil || len(collections) != 1 || collections[0].Documents != 2 {
		t.Fatalf("collections: %+v %v", collections, err)
	}
	state, err := v.Read(t.Context(), k, a)
	if err != nil || state.LatestVersion != 1 {
		t.Fatalf("data advanced code version: %+v %v", state, err)
	}
}

func TestDataStoreLimitsAndDuplicateWritesRollback(t *testing.T) {
	v, k, a, db := versionFixture(t, VersionOptions{})
	if _, err := v.Apply(t.Context(), k, versionRequest(0, "Dashboard"), a); err != nil {
		t.Fatal(err)
	}
	s := NewDataStore(db.DB, DataOptions{Enabled: true})
	valid := DataWrite{ID: "a", Value: json.RawMessage(`{"n":1}`)}
	for name, writes := range map[string][]DataWrite{
		"duplicate":           {valid, valid},
		"invalid JSON":        {{ID: "a", Value: json.RawMessage(`no`)}},
		"null":                {{ID: "a", Value: json.RawMessage(`null`)}},
		"duplicate JSON keys": {{ID: "a", Value: json.RawMessage(`{"x":1,"x":2}`)}},
		"oversize document":   {{ID: "a", Value: json.RawMessage(`{"x":"` + strings.Repeat("x", MaxDataDocumentBytes) + `"}`)}},
		"unsafe version":      {{ID: "a", IfVersion: MaxDataVersion + 1, Value: valid.Value}},
		"too many":            make([]DataWrite, MaxDataBatchWrites+1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := s.Batch(t.Context(), k, "sales", writes, a); !errors.Is(err, ErrDataInvalid) && !errors.Is(err, ErrDataQuota) {
				t.Fatalf("invalid batch accepted: %v", err)
			}
		})
	}
	collections, err := s.Collections(t.Context(), k, a)
	if err != nil || len(collections) != 0 {
		t.Fatalf("failed batches created data: %+v %v", collections, err)
	}
}

func TestDataStoreFinalAuthorityAndLifecycle(t *testing.T) {
	v, k, a, db := versionFixture(t, VersionOptions{})
	if _, err := v.Apply(t.Context(), k, versionRequest(0, "Dashboard"), a); err != nil {
		t.Fatal(err)
	}
	s := NewDataStore(db.DB, DataOptions{Enabled: true})
	writes := []DataWrite{{ID: "a", Value: json.RawMessage(`{"n":1}`)}}
	calls := 0
	denied := errors.New("revoked")
	revoke := func(ctx context.Context, tx *sql.Tx) (string, error) {
		calls++
		if calls == 2 {
			return "", denied
		}
		return a(ctx, tx)
	}
	if _, err := s.Batch(t.Context(), k, "sales", writes, revoke); !errors.Is(err, denied) {
		t.Fatalf("final write permission: %v", err)
	}
	collections, err := s.Collections(t.Context(), k, a)
	if err != nil || len(collections) != 0 {
		t.Fatalf("revoked batch survived: %+v %v", collections, err)
	}
	if _, err = s.Batch(t.Context(), k, "sales", writes, a); err != nil {
		t.Fatal(err)
	}
	calls = 0
	if docs, err := s.Read(t.Context(), k, "sales", revoke); !errors.Is(err, denied) || docs != nil {
		t.Fatalf("revoked reader received data: %+v %v", docs, err)
	}
	if docs, err := s.Read(t.Context(), VersionKey{"other-workspace", k.PageID}, "sales", a); !errors.Is(err, ErrVersionNotFound) || docs != nil {
		t.Fatalf("foreign workspace data: %+v %v", docs, err)
	}
	if _, err = NewDataStore(db.DB, DataOptions{}).Read(t.Context(), k, "sales", a); !errors.Is(err, ErrDisabled) {
		t.Fatalf("default enabled: %v", err)
	}
	if _, err = db.Exec(`DELETE FROM pages WHERE id='page'`); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = db.QueryRow(`SELECT count(*) FROM artifact_data_documents`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("deleted Page retained datasets: %d %v", count, err)
	}
}

func TestDataStoreConcurrentCASExactlyOneWinner(t *testing.T) {
	v, k, a, db := versionFixture(t, VersionOptions{})
	if _, err := v.Apply(t.Context(), k, versionRequest(0, "Dashboard"), a); err != nil {
		t.Fatal(err)
	}
	s := NewDataStore(db.DB, DataOptions{Enabled: true})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.Batch(t.Context(), k, "sales", []DataWrite{{ID: "a", Value: json.RawMessage(`{"n":1}`)}}, a)
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	success, conflict := 0, 0
	for err := range results {
		switch {
		case err == nil:
			success++
		case errors.Is(err, ErrDataConflict):
			conflict++
		default:
			t.Fatal(err)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("success=%d conflict=%d", success, conflict)
	}
}

func TestDataStoreDatasetBudgetAndRequestBudget(t *testing.T) {
	v, k, a, db := versionFixture(t, VersionOptions{})
	if _, err := v.Apply(t.Context(), k, versionRequest(0, "Dashboard"), a); err != nil {
		t.Fatal(err)
	}
	s := NewDataStore(db.DB, DataOptions{Enabled: true})
	value := json.RawMessage(`{"x":"` + strings.Repeat("x", MaxDataDocumentBytes-8) + `"}`)
	if len(value) != MaxDataDocumentBytes {
		t.Fatal("invalid limit calibration")
	}
	batch := []DataWrite{{ID: "a", Value: value}, {ID: "b", Value: value}, {ID: "c", Value: value}, {ID: "d", Value: value}}
	if _, err := s.Batch(t.Context(), k, "sales", batch, a); !errors.Is(err, ErrDataQuota) {
		t.Fatalf("batch over1MiB: %v", err)
	}
	for i := 0; i < 32; i++ {
		if _, err := s.Batch(t.Context(), k, "sales", []DataWrite{{ID: fmt.Sprintf("row%d", i), Value: value}}, a); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Batch(t.Context(), k, "sales", []DataWrite{{ID: "overflow", Value: json.RawMessage(`{"n":1}`)}}, a); !errors.Is(err, ErrDataQuota) {
		t.Fatalf("dataset over8MiB: %v", err)
	}
	collections, err := s.Collections(t.Context(), k, a)
	if err != nil || len(collections) != 1 || collections[0].Documents != 32 || collections[0].Bytes != MaxDataCollectionBytes {
		t.Fatalf("quota rollback: %+v %v", collections, err)
	}
}
