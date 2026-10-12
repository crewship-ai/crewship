package server

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestBackgroundStop_JoinsRegisteredWorkers(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	s := &Server{bgCtx: ctx, bgCancel: cancel}
	started, finish := make(chan struct{}), make(chan struct{})
	var once sync.Once
	s.RegisterBackgroundStop(func() { once.Do(func() { close(started) }); <-finish })
	stopped := make(chan struct{})
	go func() { s.StopBackground(); close(stopped) }()
	<-started
	select {
	case <-stopped:
		t.Fatal("StopBackground returned before worker joined")
	default:
	}
	close(finish)
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("StopBackground did not finish")
	}
	s.StopBackground()
	late := false
	s.RegisterBackgroundStop(func() { late = true })
	if !late {
		t.Fatal("worker registered after shutdown was not stopped")
	}
}
