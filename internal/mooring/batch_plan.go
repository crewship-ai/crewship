package mooring

// batchCycles returns exactly the vertices participating in a directed cycle,
// not descendants. Inputs and adjacency lists have stable submission order.
func batchCycles(edges [][]int) map[int]bool {
	// Tarjan's strongly connected components, including edges to vertices
	// already visited but still on the SCC stack. A DFS backedge union alone
	// misses vertices reached after another path turns their neighbor black.
	next := 0
	index, low := make([]int, len(edges)), make([]int, len(edges))
	onStack := make([]bool, len(edges))
	stack := []int{}
	cycles := map[int]bool{}
	for i := range index {
		index[i] = -1
	}
	var visit func(int)
	visit = func(v int) {
		index[v] = next
		low[v] = next
		next++
		stack = append(stack, v)
		onStack[v] = true
		for _, d := range edges[v] {
			if index[d] < 0 {
				visit(d)
				if low[d] < low[v] {
					low[v] = low[d]
				}
			} else if onStack[d] && index[d] < low[v] {
				low[v] = index[d]
			}
		}
		if low[v] != index[v] {
			return
		}
		component := []int{}
		for {
			last := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			onStack[last] = false
			component = append(component, last)
			if last == v {
				break
			}
		}
		cyclic := len(component) > 1
		if !cyclic {
			for _, d := range edges[v] {
				if d == v {
					cyclic = true
					break
				}
			}
		}
		if cyclic {
			for _, member := range component {
				cycles[member] = true
			}
		}
	}
	for v := range edges {
		if index[v] < 0 {
			visit(v)
		}
	}
	return cycles
}

type batchPlan struct {
	ByOperation map[string]int
	Edges       [][]int
	Initial     map[int]*PreparedFailure
}

func planBatch(changes []BatchChange, known map[int]BatchChangeResult, failures map[int]*PreparedFailure) batchPlan {
	p := batchPlan{ByOperation: batchOperationChanges(changes), Edges: make([][]int, len(changes)), Initial: failures}
	opIndex := map[string]int{}
	opChange := []int{}
	opPosition := map[string]int{}
	for i, c := range changes {
		for j, op := range c.Operations {
			opIndex[op.ID] = len(opChange)
			opChange = append(opChange, i)
			opPosition[op.ID] = j
		}
	}
	opEdges := make([][]int, len(opChange))
	for i, c := range changes {
		if _, exists := known[i]; exists || failures[i] != nil {
			continue
		}
		seen := map[int]bool{}
		for _, op := range c.Operations {
			for _, dep := range op.Dependencies {
				if d, ok := opIndex[dep]; ok {
					opEdges[opIndex[op.ID]] = append(opEdges[opIndex[op.ID]], d)
					other := opChange[d]
					if other != i && !seen[other] {
						p.Edges[i] = append(p.Edges[i], other)
						seen[other] = true
					}
				}
			}
		}
	}
	opCycles, changeCycles := batchCycles(opEdges), batchCycles(p.Edges)
	for index, c := range changes {
		if _, exists := known[index]; exists || failures[index] != nil {
			continue
		}
		for _, op := range c.Operations {
			cycle := opCycles[opIndex[op.ID]]
			if changeCycles[index] {
				for _, dep := range op.Dependencies {
					if other, ok := p.ByOperation[dep]; ok && other != index && changeCycles[other] {
						cycle = true
						break
					}
				}
			}
			if cycle {
				p.Initial[index] = &PreparedFailure{op.ID, batchReason("dependency_cycle")}
				break
			}
		}
		if p.Initial[index] != nil {
			continue
		}
		for j, op := range c.Operations {
			for _, dep := range op.Dependencies {
				if other, ok := p.ByOperation[dep]; ok && other == index && opPosition[dep] >= j {
					p.Initial[index] = &PreparedFailure{op.ID, batchReason("invalid_request")}
					break
				}
			}
			if p.Initial[index] != nil {
				break
			}
		}
	}
	return p
}

func dependencyReason(op BatchOperationResult) *BatchRejection {
	if op.Reason.Code == "dependency_rejected" {
		copy := *op.Reason
		root := *copy.RootCause
		copy.RootCause = &root
		return &copy
	}
	return &BatchRejection{Code: "dependency_rejected", Message: "dependency rejected", Retry: "new_operation_after_refresh", RootOperationID: op.ID, RootCause: &BatchRootCause{OperationID: op.ID, Code: op.Reason.Code, Message: op.Reason.Message}}
}
