package restrictedworkflow

import (
	"database/sql"
	"github.com/crewship-ai/crewship/internal/access"
	"github.com/crewship-ai/crewship/internal/restricteddispatch"
)

func workflowProviderAuthority(db *sql.DB) providerAuthority {
	return restricteddispatch.Authority{Store: access.Store{DB: db}}
}
