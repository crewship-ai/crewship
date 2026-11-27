package api

import (
	"encoding/json"
	"errors"
	"slices"
	"sort"
	"strconv"
	"unicode/utf8"
)

const craftRealtimeRangeLimit = 256

const craftRealtimeRangeMessage = "This change covers too many ranges. Try a smaller area."

var errCraftRealtimeTooManyRanges = errors.New("chat workbook response exceeds the range limit")

type craftSheetColumns struct{ first, last int }

type craftSheetRowEvent struct {
	row, index int
	start      bool
}

// Compact the exact union within each sheet, without visiting individual
// cells. Within a band of rows the coverage is constant. Join adjacent
// columns there, then join vertically only when both column bounds match.
func craftCompactSheetRanges(ranges []craftSheetRange) ([]craftSheetRange, error) {
	sheets := make(map[string][]nativeAgentEngineArea)
	for _, r := range ranges {
		area, ok := craftWireSheetRange(r)
		if !ok {
			return nil, errCraftRealtimeUnproved
		}
		sheets[r.Sheet] = append(sheets[r.Sheet], area)
	}
	names := make([]string, 0, len(sheets))
	for name := range sheets {
		names = append(names, name)
	}
	slices.Sort(names)
	out := make([]craftSheetRange, 0, min(len(ranges), craftRealtimeRangeLimit))
	for _, name := range names {
		areas := sheets[name]
		events := make([]craftSheetRowEvent, 0, 2*len(areas))
		for i, a := range areas {
			events = append(events, craftSheetRowEvent{a.row1, i, true}, craftSheetRowEvent{a.row2 + 1, i, false})
		}
		sort.Slice(events, func(i, j int) bool { return events[i].row < events[j].row })
		rectangles := make([]nativeAgentEngineArea, 0, min(len(areas), craftRealtimeRangeLimit))
		previous, next := make(map[craftSheetColumns]int), make(map[craftSheetColumns]int)
		active := make(map[int]nativeAgentEngineArea)
		var columns []craftSheetColumns
		for i := 0; i < len(events); {
			first := events[i].row
			for i < len(events) && events[i].row == first {
				event := events[i]
				if event.start {
					active[event.index] = areas[event.index]
				} else {
					delete(active, event.index)
				}
				i++
			}
			if i == len(events) {
				break
			}
			last := events[i].row - 1
			columns = columns[:0]
			for _, a := range active {
				columns = append(columns, craftSheetColumns{a.col1, a.col2})
			}
			sort.Slice(columns, func(i, j int) bool { return columns[i].first < columns[j].first })
			clear(next)
			for j := 0; j < len(columns); {
				c := columns[j]
				j++
				for j < len(columns) && columns[j].first <= c.last+1 {
					c.last = max(c.last, columns[j].last)
					j++
				}
				if index, joined := previous[c]; joined {
					rectangles[index].row2 = last
					next[c] = index
					continue
				}
				if len(out)+len(rectangles) >= craftRealtimeRangeLimit {
					return nil, errCraftRealtimeTooManyRanges
				}
				next[c] = len(rectangles)
				rectangles = append(rectangles, nativeAgentEngineArea{col1: c.first, row1: first, col2: c.last, row2: last})
			}
			previous, next = next, previous
		}
		for _, a := range rectangles {
			start, end := craftUndoA1(a.col1, a.row1), craftUndoA1(a.col2, a.row2)
			if start != end {
				start += ":" + end
			}
			out = append(out, craftSheetRange{Sheet: name, A1: start})
		}
	}
	return out, nil
}

func craftWireSheetRange(r craftSheetRange) (nativeAgentEngineArea, bool) {
	if r.Sheet == "" || len(r.Sheet) > 128 || !utf8.ValidString(r.Sheet) {
		return nativeAgentEngineArea{}, false
	}
	for _, ch := range r.Sheet {
		if ch < 32 || ch == 127 {
			return nativeAgentEngineArea{}, false
		}
	}
	return nativeAgentEngineRange(json.RawMessage(strconv.Quote(r.A1)))
}

// Input effects are observed from populated XML cells, never expanded from a
// rectangle. Group adjacent observations before compacting vertical bands.
func craftRealtimeRowRanges(cells []craftRealtimeCell) []craftSheetRange {
	sort.Slice(cells, func(i, j int) bool {
		a, b := cells[i], cells[j]
		if a.sheet != b.sheet {
			return a.sheet < b.sheet
		}
		if a.row != b.row {
			return a.row < b.row
		}
		return a.col < b.col
	})
	out := make([]craftSheetRange, 0, len(cells))
	for i := 0; i < len(cells); {
		first, last := cells[i], cells[i]
		i++
		for i < len(cells) && cells[i].sheet == last.sheet && cells[i].row == last.row && cells[i].col == last.col+1 {
			last = cells[i]
			i++
		}
		r := first.wireRange()
		if first != last {
			r.A1 += ":" + craftUndoA1(last.col, last.row)
		}
		out = append(out, r)
	}
	return out
}
