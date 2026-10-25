package server

import (
	"math"
	"sort"
	"strconv"
	"strings"
)

// maxViews is how many filter/sort orderings are remembered per table.
const maxViews = 6

// term is one clause of a search box query. Clauses are combined with AND.
type term struct {
	col    int // -1 searches every column
	op     string
	text   string // already lower-cased
	num    float64
	hasNum bool
	negate bool
}

// parseQuery turns what the reader typed into clauses.
//
//	berlin              any cell containing "berlin"
//	city:berlin         that column only
//	"new york"          a phrase
//	amount>1000         numeric comparison on a numeric or date column
//	-cancelled          rows that do not mention it
func parseQuery(t *Table, q string) []term {
	var terms []term
	for _, tok := range splitQuery(q) {
		if tok == "" {
			continue
		}
		tm := term{col: -1, op: "contains"}
		if strings.HasPrefix(tok, "-") && len(tok) > 1 {
			tm.negate = true
			tok = tok[1:]
		}

		if col, op, rest, ok := splitClause(t, tok); ok {
			tm.col, tm.op, tok = col, op, rest
		}
		tm.text = strings.ToLower(unquote(tok))
		if f, ok := parseNumber(tm.text, false); ok {
			tm.num, tm.hasNum = f, true
		} else if f, ok := parseNumber(tm.text, true); ok {
			tm.num, tm.hasNum = f, true
		} else if tm.col >= 0 && t.Columns[tm.col].Kind == kindDate {
			if ts, ok := parseDateWith(tm.text, t.Columns[tm.col].DateLayout); ok {
				tm.num, tm.hasNum = float64(ts.Unix()), true
			}
		}
		if tm.op != "contains" && !tm.hasNum {
			tm.op = "contains" // a comparison against text is just a match
		}
		if tm.text == "" && tm.op == "contains" {
			continue
		}
		terms = append(terms, tm)
	}
	return terms
}

// splitClause recognises "column:value" and "column>=value" prefixes.
func splitClause(t *Table, tok string) (int, string, string, bool) {
	for _, op := range []string{">=", "<=", "!=", ":", "=", ">", "<"} {
		i := strings.Index(tok, op)
		if i <= 0 {
			continue
		}
		name := strings.ToLower(unquote(tok[:i]))
		col := findColumn(t, name)
		if col < 0 {
			continue
		}
		normalised := op
		if op == ":" {
			normalised = "contains"
		}
		return col, normalised, tok[i+len(op):], true
	}
	return -1, "", tok, false
}

// findColumn matches a column by exact name first, then by unique prefix.
func findColumn(t *Table, name string) int {
	if name == "" {
		return -1
	}
	for i, c := range t.Columns {
		if strings.EqualFold(c.Name, name) {
			return i
		}
	}
	hit := -1
	for i, c := range t.Columns {
		if strings.HasPrefix(strings.ToLower(c.Name), name) {
			if hit >= 0 {
				return -1 // ambiguous
			}
			hit = i
		}
	}
	return hit
}

func unquote(s string) string {
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		return s[1 : len(s)-1]
	}
	return s
}

// splitQuery breaks the query on spaces, keeping quoted phrases whole.
func splitQuery(q string) []string {
	var (
		out   []string
		cur   strings.Builder
		quote bool
	)
	for _, r := range q {
		switch {
		case r == '"':
			quote = !quote
			cur.WriteRune(r)
		case (r == ' ' || r == '\t') && !quote:
			out = append(out, cur.String())
			cur.Reset()
		default:
			cur.WriteRune(r)
		}
	}
	out = append(out, cur.String())
	return out
}

// matches reports whether a row satisfies every clause.
func (t *Table) matches(row int, terms []term) bool {
	for _, tm := range terms {
		ok := t.matchTerm(row, tm)
		if tm.negate {
			ok = !ok
		}
		if !ok {
			return false
		}
	}
	return true
}

func (t *Table) matchTerm(row int, tm term) bool {
	if tm.op == "contains" {
		if tm.col < 0 {
			for _, cell := range t.Rows[row] {
				if strings.Contains(strings.ToLower(cell), tm.text) {
					return true
				}
			}
			return false
		}
		return strings.Contains(strings.ToLower(t.cell(row, tm.col)), tm.text)
	}

	keys := t.sortKeys(tm.col)
	v := keys[row]
	if math.IsNaN(v) {
		return false
	}
	switch tm.op {
	case ">":
		return v > tm.num
	case ">=":
		return v >= tm.num
	case "<":
		return v < tm.num
	case "<=":
		return v <= tm.num
	case "=":
		return v == tm.num
	case "!=":
		return v != tm.num
	}
	return false
}

// view returns the row indices matching q, ordered by the given column. A
// sortCol below zero keeps file order. Results are memoised, because scrolling
// a large table asks for the same ordering over and over.
func (t *Table) view(q string, sortCol int, dir string) []int32 {
	if dir != "desc" {
		dir = "asc"
	}
	key := q + "\x00" + strconv.Itoa(sortCol) + "\x00" + dir

	t.mu.Lock()
	if idx, ok := t.views[key]; ok {
		t.mu.Unlock()
		return idx
	}
	t.mu.Unlock()

	idx := t.filter(q)
	if sortCol >= 0 && sortCol < len(t.Columns) {
		idx = t.sorted(idx, sortCol, dir)
	}

	t.mu.Lock()
	t.views[key] = idx
	t.viewLRU = append(t.viewLRU, key)
	for len(t.viewLRU) > maxViews {
		delete(t.views, t.viewLRU[0])
		t.viewLRU = t.viewLRU[1:]
	}
	t.mu.Unlock()
	return idx
}

func (t *Table) filter(q string) []int32 {
	terms := parseQuery(t, strings.TrimSpace(q))
	if len(terms) == 0 {
		all := make([]int32, len(t.Rows))
		for i := range all {
			all[i] = int32(i)
		}
		return all
	}
	out := make([]int32, 0, len(t.Rows)/4+8)
	for i := range t.Rows {
		if t.matches(i, terms) {
			out = append(out, int32(i))
		}
	}
	return out
}

// sorted orders a set of row indices by one column. Blank cells always sink to
// the bottom, in both directions, the way a spreadsheet sorts them.
func (t *Table) sorted(idx []int32, col int, dir string) []int32 {
	out := make([]int32, len(idx))
	copy(out, idx)
	desc := dir == "desc"
	kind := t.Columns[col].Kind

	if kind == kindText {
		sort.SliceStable(out, func(a, b int) bool {
			x := trimValue(t.cell(int(out[a]), col))
			y := trimValue(t.cell(int(out[b]), col))
			if x == "" || y == "" {
				return x != "" && y == ""
			}
			c := naturalCompare(x, y)
			if desc {
				return c > 0
			}
			return c < 0
		})
		return out
	}

	keys := t.sortKeys(col)
	sort.SliceStable(out, func(a, b int) bool {
		x, y := keys[out[a]], keys[out[b]]
		xn, yn := math.IsNaN(x), math.IsNaN(y)
		if xn || yn {
			return !xn && yn
		}
		if desc {
			return x > y
		}
		return x < y
	})
	return out
}
