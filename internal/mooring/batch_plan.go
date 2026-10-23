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
	Initial     map[int]*BatchRejection
}

func planBatch(changes []BatchChange, known map[int]BatchChangeResult) batchPlan {
	p := batchPlan{ByOperation: map[string]int{}, Edges: make([][]int, len(changes)), Initial: map[int]*BatchRejection{}}
	opIndex := map[string]int{}
	opChange := []int{}
	opPosition := map[string]int{}
	for i, c := range changes {
		for j, op := range c.Operations {
			p.ByOperation[op.ID] = i
			opIndex[op.ID] = len(opChange)
			opChange = append(opChange, i)
			opPosition[op.ID] = j
		}
	}
	opEdges := make([][]int, len(opChange))
	for i, c := range changes {
		if _, exists := known[i]; exists {
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
	for index := range batchCycles(opEdges) {
		p.Initial[opChange[index]] = batchReason("dependency_cycle")
	}
	for index := range batchCycles(p.Edges) {
		p.Initial[index] = batchReason("dependency_cycle")
	}
	for i, c := range changes {
		if _, exists := known[i]; exists {
			continue
		}
		if p.Initial[i] != nil {
			continue
		}
		for j, op := range c.Operations {
			for _, dep := range op.Dependencies {
				if index, ok := p.ByOperation[dep]; ok && index == i && opPosition[dep] >= j {
					p.Initial[i] = batchReason("invalid_request")
				}
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
