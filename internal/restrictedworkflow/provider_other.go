//go:build !linux

package restrictedworkflow

import "database/sql"

func workflowProviderAuthority(*sql.DB) providerAuthority { return nil }
