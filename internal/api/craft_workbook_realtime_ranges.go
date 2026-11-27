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
		rows := make([]int, 0, 2*len(areas))
		for _, a := range areas {
			rows = append(rows, a.row1, a.row2+1)
		}
		slices.Sort(rows)
		rows = slices.Compact(rows)
		rectangles := make([]nativeAgentEngineArea, 0, len(areas))
		previous := make(map[craftSheetColumns]int)
		columns := make([]craftSheetColumns, 0, len(areas))
		for i := 0; i+1 < len(rows); i++ {
			first, last := rows[i], rows[i+1]-1
			columns = columns[:0]
			for _, a := range areas {
				if a.row1 <= first && first <= a.row2 {
					columns = append(columns, craftSheetColumns{a.col1, a.col2})
				}
			}
			sort.Slice(columns, func(i, j int) bool { return columns[i].first < columns[j].first })
			next := make(map[craftSheetColumns]int)
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
			previous = next
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
