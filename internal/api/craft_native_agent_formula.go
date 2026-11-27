package api

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

// nativeAgentFormulaFunctions is the whole set of functions an agent-written
// formula may call. It holds pure calculation, logic, lookup, text and date
// functions. It leaves out everything that reaches outside the workbook
// (WEBSERVICE, HYPERLINK, DDE, RTD, CALL, REGISTER, FILTERXML, IMPORT*),
// everything that reads the clock or randomness (NOW, TODAY, RAND*) and the
// reflective ones (INDIRECT, OFFSET, INFO, CELL, FORMULATEXT).
var nativeAgentFormulaFunctions = func() map[string]bool {
	set := map[string]bool{}
	for _, name := range strings.Fields(`
		ABS AND AVERAGE AVERAGEIF AVERAGEIFS CHOOSE COLUMN COLUMNS CONCAT CONCATENATE
		COUNT COUNTA COUNTBLANK COUNTIF COUNTIFS DATE DAY EDATE EOMONTH EXACT FALSE
		FIND HLOOKUP IF IFERROR IFNA IFS INDEX INT ISBLANK ISERROR ISNUMBER ISTEXT
		LARGE LEFT LEN LOWER MATCH MAX MAXIFS MEDIAN MID MIN MINIFS MOD MONTH NOT OR
		POWER PRODUCT PROPER RANK RIGHT ROUND ROUNDDOWN ROUNDUP ROW ROWS SEARCH
		SMALL SQRT SUBSTITUTE SUBTOTAL SUM SUMIF SUMIFS SUMPRODUCT SWITCH TEXT
		TEXTJOIN TRIM TRUE UPPER VALUE VLOOKUP XLOOKUP XMATCH YEAR`) {
		set[name] = true
	}
	return set
}()

// nativeAgentEngineFormula reports whether text is a formula the agent may
// write: one expression starting with "=", at most 8 KiB, calling only
// allowlisted functions, with no reference outside this workbook. Text inside
// double quotes is data and is skipped. Brackets are refused outright: they
// carry external workbook links ([1]Sheet!A1, '[file.xlsx]Sheet'!A1) and
// structured table references.
func nativeAgentEngineFormula(text string) bool {
	body := strings.TrimSpace(text)
	if !strings.HasPrefix(body, "=") || len(body) > 8<<10 || !utf8.ValidString(body) {
		return false
	}
	runes := []rune(body[1:])
	depth := 0
	var code strings.Builder // the formula without quoted text, for the range rules
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		start := i
		switch {
		case r < 0x20 || r == 0x7f:
			return false
		case r == '(':
			if depth++; depth > nativeAgentFormulaMaxDepth {
				return false
			}
		case r == ')':
			if depth--; depth < 0 {
				return false
			}
		case r == '"':
			closed := false
			for i++; i < len(runes); i++ {
				if runes[i] != '"' {
					continue
				}
				if i+1 < len(runes) && runes[i+1] == '"' { // doubled quote inside the text
					i++
					continue
				}
				closed = true
				break
			}
			if !closed {
				return false
			}
		case r == '[' || r == ']' || r == '@' || r == '\\':
			return false
		case r == '\'':
			// A quoted sheet name: letters, digits, spaces and the like up to
			// the closing quote, with the engine's own rule for doubled quotes.
			closed := false
			for i++; i < len(runes); i++ {
				if runes[i] == '\'' {
					if i+1 < len(runes) && runes[i+1] == '\'' {
						i++
						continue
					}
					closed = true
					break
				}
				if strings.ContainsRune("[]:*?/\\", runes[i]) {
					return false
				}
			}
			if !closed {
				return false
			}
		case isFormulaNameStart(r):
			j := i
			for j < len(runes) && isFormulaNamePart(runes[j]) {
				j++
			}
			word := strings.ToUpper(string(runes[i:j]))
			k := j
			for k < len(runes) && runes[k] == ' ' {
				k++
			}
			switch {
			case k < len(runes) && runes[k] == '(':
				// A call. A dotted or namespaced name is never on the list.
				if !nativeAgentFormulaFunctions[word] {
					return false
				}
			case k < len(runes) && runes[k] == '!':
				// A sheet qualifier (Sheet2!A1).
			case word == "TRUE" || word == "FALSE":
			case nativeAgentFormulaReference(word, runes, i, j):
			default:
				// Anything else is a defined name or an R1C1 reference. A name
				// can stand for any formula, including a forbidden function.
				return false
			}
			i = j - 1
		}
		if runes[start] == '"' || runes[start] == '\'' {
			code.WriteString(strings.Repeat(" ", i+1-start))
		} else {
			code.WriteString(string(runes[start : i+1]))
		}
	}
	return depth == 0 && nativeAgentFormulaRangesAreBounded(strings.ToUpper(code.String()))
}

const nativeAgentFormulaMaxDepth = 64

// nativeAgentFormulaReference is a cell address (A1, $B$2) or a column letter
// group next to a colon (A:A, A:C).
func nativeAgentFormulaReference(word string, runes []rune, from, to int) bool {
	plain := strings.ReplaceAll(word, "$", "")
	if _, _, ok := nativeAgentEngineAddress(plain); ok {
		return true
	}
	if len(plain) == 0 || len(plain) > 3 {
		return false
	}
	for _, c := range plain {
		if c < 'A' || c > 'Z' {
			return false
		}
	}
	return from > 0 && runes[from-1] == ':' || to < len(runes) && runes[to] == ':'
}

var (
	nativeAgentFormulaRectangle = regexp.MustCompile(`\$?([A-Z]{1,3})\$?([0-9]+):\$?([A-Z]{1,3})\$?([0-9]+)`)
	nativeAgentFormulaColumns   = regexp.MustCompile(`\$?([A-Z]{1,3}):\$?([A-Z]{1,3})`)
	nativeAgentFormulaRows      = regexp.MustCompile(`(?:^|[^A-Z0-9$])\$?([0-9]+):\$?([0-9]+)`)
)

// nativeAgentFormulaRangesAreBounded keeps one formula from asking the engine
// for a sheet-sized calculation: rectangles of at most two million cells,
// column ranges of at most 64 columns, row ranges of at most 1000 rows, and no column
// range at all inside SUMPRODUCT.
func nativeAgentFormulaRangesAreBounded(code string) bool {
	for _, m := range nativeAgentFormulaRectangle.FindAllStringSubmatch(code, -1) {
		c1, r1, ok1 := nativeAgentEngineAddress(m[1] + m[2])
		c2, r2, ok2 := nativeAgentEngineAddress(m[3] + m[4])
		if !ok1 || !ok2 || c2 < c1 || r2 < r1 || (c2-c1+1)*(r2-r1+1) > 2_000_000 {
			return false
		}
	}
	columns := nativeAgentFormulaColumns.FindAllStringSubmatch(code, -1)
	for _, m := range columns {
		c1, _, ok1 := nativeAgentEngineAddress(m[1] + "1")
		c2, _, ok2 := nativeAgentEngineAddress(m[2] + "1")
		if !ok1 || !ok2 || c2 < c1 || c2-c1+1 > 64 {
			return false
		}
	}
	if len(columns) > 0 && strings.Contains(code, "SUMPRODUCT") {
		return false
	}
	for _, m := range nativeAgentFormulaRows.FindAllStringSubmatch(code, -1) {
		a, b := atoiBounded(m[1]), atoiBounded(m[2])
		if a < 1 || b < a || b-a+1 > 1000 {
			return false
		}
	}
	return true
}

func atoiBounded(s string) int {
	n := 0
	for _, c := range s {
		if n = n*10 + int(c-'0'); n > 1<<30 {
			return 1 << 30
		}
	}
	return n
}

func isFormulaNameStart(r rune) bool {
	return r == '_' || r == '$' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r > 0x7f
}

func isFormulaNamePart(r rune) bool {
	return isFormulaNameStart(r) || r >= '0' && r <= '9' || r == '.'
}
