package mooring

// batchCycles returns exactly the vertices participating in a directed cycle,
// not descendants. Inputs and adjacency lists have stable submission order.
func batchCycles(edges [][]int) map[int]bool {
	state := make([]uint8, len(edges))
	stack := []int{}
	cycles := map[int]bool{}
	var visit func(int)
	visit = func(v int) {
		state[v] = 1
		stack = append(stack, v)
		for _, d := range edges[v] {
			if state[d] == 0 {
				visit(d)
			} else if state[d] == 1 {
				for i := len(stack) - 1; i >= 0; i-- {
					cycles[stack[i]] = true
					if stack[i] == d {
						break
					}
				}
			}
		}
		stack = stack[:len(stack)-1]
		state[v] = 2
	}
	for v := range edges {
		if state[v] == 0 {
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
