package backup

import "errors"

// Retry lineage is a DAG. Its INSERT scope guard needs the parent present even
// with deferred FKs; backup row order and freshly reminted IDs are not ancestry.
func orderPageRequestRetryRows(table string, rows []map[string]any) ([]map[string]any, error) {
	if table != "page_artifact_requests" && table != "page_board_requests" {
		return rows, nil
	}
	byID := map[string]int{}
	for i, row := range rows {
		if id, ok := row["id"].(string); ok {
			byID[id] = i
		}
	}
	children := map[int][]int{}
	pending := make([]bool, len(rows))
	queue := []int{}
	for i, row := range rows {
		parent, _ := row["retry_of_request_id"].(string)
		if p, ok := byID[parent]; ok && parent != "" {
			pending[i] = true
			children[p] = append(children[p], i)
		} else {
			queue = append(queue, i)
		}
	}
	out := make([]map[string]any, 0, len(rows))
	for n := 0; n < len(queue); n++ {
		i := queue[n]
		out = append(out, rows[i])
		for _, child := range children[i] {
			if pending[child] {
				pending[child] = false
				queue = append(queue, child)
			}
		}
	}
	if len(out) != len(rows) {
		return nil, errors.New("backup: cyclic page request retry lineage")
	}
	return out, nil
}
