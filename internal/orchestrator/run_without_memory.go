package orchestrator

import "context"

// WithoutMemory is for a run a human message bound to attached artifacts: it
// reads no memory (agent, crew, workspace, episodic recall) and is offered no
// memory tools, so what it reads in those artifacts cannot become memory that
// another person's chat later sees. The chat itself is unchanged: its stream,
// history and answer. The server refuses such a run's memory writes as well;
// this is the half that does not offer them.
func (r AgentRunRequest) WithoutMemory() AgentRunRequest {
	r.withoutMemory = true
	r.MemoryEnabled = false
	return r
}

type memoryWithheldKey struct{}

// memoryWithheld reports whether the run in ctx is offered no memory.
func memoryWithheld(ctx context.Context) bool {
	v, _ := ctx.Value(memoryWithheldKey{}).(bool)
	return v
}

// memoryContext carries the choice to everything the run sets up from ctx
// (the MCP writers ask memorySinkReady).
func (r AgentRunRequest) memoryContext(ctx context.Context) context.Context {
	if !r.withoutMemory {
		return ctx
	}
	return context.WithValue(ctx, memoryWithheldKey{}, true)
}
