//go:build !linux

package appengine

import (
	"context"
	"net"
)

// The qualified worker transport requires Linux peer credentials.
func dialPrivateWorker(context.Context, string) (net.Conn, error) { return nil, ErrEngineUnavailable }
