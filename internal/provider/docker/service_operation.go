package docker

import (
	"context"
	"sync"
	"time"

	"github.com/moby/moby/client"

	"github.com/crewship-ai/crewship/internal/provider"
)

type serviceOperationGate struct {
	mu   sync.RWMutex
	gate provider.ServiceOperationGate
}

// SetServiceOperationGate is host bootstrap configuration. No request or
// declaration can select or disable this gate.
func (p *Provider) SetServiceOperationGate(gate provider.ServiceOperationGate) {
	p.serviceOperation.mu.Lock()
	defer p.serviceOperation.mu.Unlock()
	p.serviceOperation.gate = gate
}
func (p *Provider) serviceGate() provider.ServiceOperationGate {
	p.serviceOperation.mu.RLock()
	defer p.serviceOperation.mu.RUnlock()
	return p.serviceOperation.gate
}

// Re-admit immediately before Start: a delayed Create response must never use
// an expired admission to start writing after maintenance recovery took over.
func (p *Provider) startAdmittedService(ctx context.Context, crew, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	gate := p.serviceGate()
	if gate == nil {
		_, err := p.client.ContainerStart(ctx, id, client.ContainerStartOptions{})
		return err
	}
	release, err := gate(ctx, crew)
	if err != nil {
		return err
	}
	_, err = p.client.ContainerStart(ctx, id, client.ContainerStartOptions{})
	if err != nil {
		return err
	} // uncertain Start retains the bounded operation lease
	cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer stop()
	return release(cleanup)
}
