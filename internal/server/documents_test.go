package server

import (
	"context"
	"errors"
	"testing"
)

func TestDocumentsStartupFailurePreventsServing(t *testing.T) {
	cause := errors.New("private authority listener failed")
	s := &Server{documentsStartupErr: cause}
	if err := s.Start(context.Background()); !errors.Is(err, cause) {
		t.Fatalf("configured Docs startup failure hidden: %v", err)
	}
}
