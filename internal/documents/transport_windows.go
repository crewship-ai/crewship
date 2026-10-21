//go:build windows

package documents

import (
	"context"
	"errors"
	"net/http"
)

func ServePrivateUnix(_ string, _ http.Handler, _ ...func() error) (func(context.Context) error, error) {
	return nil, errors.New("Docs authority requires Unix sockets")
}
