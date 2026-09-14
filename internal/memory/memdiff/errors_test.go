package memdiff

import "errors"

// asRemovalError is errors.As with the concrete type spelled out, kept in one
// place so the test files do not each restate it.
func asRemovalError(err error, target **RemovalError) bool { return errors.As(err, target) }
