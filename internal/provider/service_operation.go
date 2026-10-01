package provider

import "context"

// ServiceOperationGate is installed only by the host. Admission serializes
// service starts against a durable backup fence across database connections.
// Release must use a fresh bounded context when the operation was canceled.
type ServiceOperationGate func(context.Context, string) (func(context.Context) error, error)
