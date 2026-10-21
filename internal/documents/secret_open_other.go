//go:build !unix

package documents

import (
	"errors"
	"os"
)

func openServiceSecret(string) (*os.File, error) {
	return nil, errors.New("Docs service secret files require the Unix service transport")
}
