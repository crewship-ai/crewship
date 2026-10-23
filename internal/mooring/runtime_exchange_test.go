package mooring

import (
	"bytes"
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"
)

func TestRuntimeExchangeCancellationBeforeTurnDoesNotCloseActiveRequest(t *testing.T) {
	parent, child := net.Pipe()
	t.Cleanup(func() { _ = parent.Close(); _ = child.Close() })
	r := &Runtime{RPC: &RPC{conn: parent}}
	peer := &RPC{conn: child}
	read := make(chan struct{})
	release := make(chan struct{})
	serverDone := make(chan error, 1)
	go func() {
		frame, err := peer.Read()
		close(read)
		if err == nil {
			<-release
			err = peer.Write(frame)
		}
		serverDone <- err
	}()
	active := make(chan error, 1)
	go func() {
		_, err := r.Exchange(t.Context(), []byte(`{"request_id":"active"}`))
		active <- err
	}()
	<-read
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := r.Exchange(ctx, []byte(`{"request_id":"cancelled-before-turn"}`)); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled queued request did not return", err)
	}
	close(release)
	if err := <-active; err != nil {
		t.Fatal("queued cancellation poisoned active exchange", err)
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeExchangeCancellationClosesLateReplyPath(t *testing.T) {
	parent, child := net.Pipe()
	t.Cleanup(func() { _ = parent.Close(); _ = child.Close() })
	r := &Runtime{RPC: &RPC{conn: parent}}
	peer := &RPC{conn: child}
	read := make(chan struct{})
	go func() {
		_, _ = peer.Read()
		close(read)
	}()
	ctx, cancel := context.WithCancel(t.Context())
	result := make(chan error, 1)
	go func() {
		_, err := r.Exchange(ctx, []byte(`{"request_id":"lost"}`))
		result <- err
	}()
	<-read
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled exchange stayed blocked")
	}
	if err := peer.Write([]byte(`{"request_id":"lost"}`)); err == nil {
		t.Fatal("late response remained deliverable")
	}
	if _, err := r.Exchange(t.Context(), []byte(`{"request_id":"later"}`)); err == nil {
		t.Fatal("poisoned channel was reused")
	}
}

func TestRuntimeExchangeDeadlineClosesTransport(t *testing.T) {
	parent, child := net.Pipe()
	t.Cleanup(func() { _ = parent.Close(); _ = child.Close() })
	r := &Runtime{RPC: &RPC{conn: parent}}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	if _, err := r.Exchange(ctx, []byte(`{"request_id":"blocked-write"}`)); err == nil {
		t.Fatal("blocked write exceeded deadline")
	}
	if err := r.RPC.Write([]byte(`{}`)); err == nil {
		t.Fatal("timed out transport reused")
	}
}

// Uses a real stream with concurrent callers. Correlation/schema validation is
// intentionally not mocked by Exchange: each caller must receive its own bytes.
func TestRuntimeExchangeConcurrentPairing(t *testing.T) {
	parent, child := net.Pipe()
	t.Cleanup(func() { _ = parent.Close(); _ = child.Close() })
	r := &Runtime{RPC: &RPC{conn: parent}}
	peer := &RPC{conn: child}
	const count = 32
	serverDone := make(chan error, 1)
	go func() {
		for range count {
			frame, err := peer.Read()
			if err == nil {
				err = peer.Write(frame)
			}
			if err != nil {
				serverDone <- err
				return
			}
		}
		serverDone <- nil
	}()
	var wg sync.WaitGroup
	for i := range count {
		wg.Go(func() {
			request := bytes.Repeat([]byte{' '}, i)
			request = append(request, []byte(`{"type":"pairing-test"}`)...)
			response, err := r.Exchange(t.Context(), request)
			if err != nil || !bytes.Equal(request, response) {
				t.Error("request and response were interleaved", i, err)
			}
		})
	}
	wg.Wait()
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
}
