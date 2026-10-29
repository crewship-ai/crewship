package conversation

import (
	"errors"
	"github.com/crewship-ai/crewship/internal/chataudience"
	"sync"
	"testing"

	"github.com/crewship-ai/crewship/internal/documents"
	"github.com/crewship-ai/crewship/internal/logging"
)

func TestDocumentCreationCommittedSubmissionReplayNeverAppends(t *testing.T) {
	for _, change := range []string{"exact", "content", "drafting", "revoked-receipt", "revoked-consent"} {
		t.Run(change, func(t *testing.T) {
			s, receipt, intent, msg := creationStore(t, true)
			if err := s.AppendDocumentCreation(t.Context(), receipt.ChatID, msg, intent, receipt); err != nil {
				t.Fatal(err)
			}
			var err error
			receipt, err = chataudience.CaptureTrusted(t.Context(), s.db, receipt.ChatID, receipt.UserID, nil)
			if err != nil {
				t.Fatal(err)
			}
			retry := msg
			retry.ID = "new-transport-message-id"
			switch change {
			case "content":
				retry.Content += " changed"
			case "drafting":
				intent.Drafting = false
				retry.Metadata = map[string]any{documents.CreationMetadataKey: intent}
			case "revoked-consent":
				if _, err := s.db.Exec(`UPDATE document_creation_consents SET revoked_at='2026-10-21T00:00:00Z'`); err != nil {
					t.Fatal(err)
				}
			case "revoked-receipt":
				if _, err := s.db.Exec(`UPDATE workspace_members SET access_revision=access_revision+1 WHERE id='creation-member'`); err != nil {
					t.Fatal(err)
				}
			}
			err = s.AppendDocumentCreation(t.Context(), receipt.ChatID, retry, intent, receipt)
			var accepted *DocumentCreationAlreadyAccepted
			switch change {
			case "exact":
				if !errors.As(err, &accepted) || accepted.MessageID != msg.ID {
					t.Fatalf("exact replay: %v", err)
				}
			case "content", "drafting":
				if !errors.Is(err, ErrDocumentCreationSubmissionConflict) {
					t.Fatalf("changed replay: %v", err)
				}
			default:
				if err == nil || errors.As(err, &accepted) {
					t.Fatalf("revoked replay disclosed accepted state: %v", err)
				}
			}
			s.Close()
			reopened := NewStore(s.basePath, logging.New("error", "json", nil), WithDB(s.db))
			t.Cleanup(reopened.Close)
			transcript, err := reopened.Read(t.Context(), receipt.ChatID, 0, 0)
			if err != nil || len(transcript) != 1 || transcript[0].ID != msg.ID {
				t.Fatalf("committed submission replay appended durable second effect: %+v %v", transcript, err)
			}
			if n := creationCount(t, s.db, `SELECT count(*) FROM document_creation_consents`); n != 1 {
				t.Fatalf("consents%d", n)
			}
			if n := creationCount(t, s.db, `SELECT count(*) FROM conversation_messages`); n != 1 {
				t.Fatalf("mirror%d", n)
			}
		})
	}
}

func TestDocumentCreationParallelStoresReserveSubmissionBeforeJSONL(t *testing.T) {
	first, receipt, intent, msg := creationStore(t, true)
	second := NewStore(first.basePath, logging.New("error", "json", nil), WithDB(first.db))
	t.Cleanup(second.Close)
	start := make(chan struct{})
	outcomes := make(chan error, 2)
	var wg sync.WaitGroup
	for i, store := range []*Store{first, second} {
		wg.Add(1)
		go func(i int, s *Store) {
			defer wg.Done()
			<-start
			m := msg
			if i == 1 {
				m.ID = "concurrent-other-message"
			}
			outcomes <- s.AppendDocumentCreation(t.Context(), receipt.ChatID, m, intent, receipt)
		}(i, store)
	}
	close(start)
	wg.Wait()
	close(outcomes)
	accepted := 0
	for err := range outcomes {
		if err == nil {
			accepted++
		}
	}
	if accepted != 1 {
		t.Fatalf("accepted%d", accepted)
	}
	first.Close()
	second.Close()
	reopened := NewStore(first.basePath, logging.New("error", "json", nil), WithDB(first.db))
	t.Cleanup(reopened.Close)
	lines, err := reopened.Read(t.Context(), receipt.ChatID, 0, 0)
	if err != nil || len(lines) != 1 {
		t.Fatalf("parallel exact submission left %d durable lines: %v", len(lines), err)
	}
	if n := creationCount(t, first.db, `SELECT count(*) FROM document_creation_consents`); n != 1 {
		t.Fatalf("consents%d", n)
	}
}
