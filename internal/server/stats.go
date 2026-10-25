package server

import (
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
)

// distinctCap bounds the memory a stats request can cost on a wide table of
// unique values.
const distinctCap = 100_000

// histBuckets is the resolution of the little distribution bar chart.
const histBuckets = 28

// ValueCount is one row of the "most common values" list.
type ValueCount struct {
	Value string `json:"value"`
	Count int    `json:"count"`
}

// NumStats describes a numeric or date column over the rows currently shown.
type NumStats struct {
	Min     float64 `json:"min"`
	Max     float64 `json:"max"`
	Mean    float64 `json:"mean"`
	Median  float64 `json:"median"`
	Sum     float64 `json:"sum"`
	StdDev  float64 `json:"stddev"`
	MinText string  `json:"minText"`
	MaxText string  `json:"maxText"`
	Hist    []int   `json:"hist"`
	HistLo  float64 `json:"histLo"`
	HistHi  float64 `json:"histHi"`
}

// ColStats is what the column menu shows.
type ColStats struct {
	Column         string       `json:"column"`
	Kind           string       `json:"kind"`
	Rows           int          `json:"rows"`
	Filled         int          `json:"filled"`
	Empty          int          `json:"empty"`
	Distinct       int          `json:"distinct"`
	DistinctCapped bool         `json:"distinctCapped"`
	Unparsed       int          `json:"unparsed"`
	Top            []ValueCount `json:"top"`
	Num            *NumStats    `json:"num,omitempty"`
}

// Stats summarises one column across a set of rows — the filtered set, so the
// numbers always describe what is on screen.
func (t *Table) Stats(col int, idx []int32) ColStats {
	c := t.Columns[col]
	out := ColStats{Column: c.Name, Kind: c.Kind, Rows: len(idx)}

	counts := make(map[string]int, 64)
	capped := false
	var values []float64

	numeric := c.Kind == kindInt || c.Kind == kindNumber || c.Kind == kindDate
	var keys []float64
	if numeric {
		keys = t.sortKeys(col)
	}

	for _, r := range idx {
		v := trimValue(t.cell(int(r), col))
		if v == "" {
			out.Empty++
			continue
		}
		out.Filled++
		if len(counts) < distinctCap {
			counts[v]++
		} else if _, ok := counts[v]; ok {
			counts[v]++
		} else {
			capped = true
		}
		if numeric {
			k := keys[r]
			if math.IsNaN(k) {
				out.Unparsed++
			} else {
				values = append(values, k)
			}
		}
	}

	out.Distinct = len(counts)
	out.DistinctCapped = capped

	type kv struct {
		k string
		n int
	}
	pairs := make([]kv, 0, len(counts))
	for k, n := range counts {
		pairs = append(pairs, kv{k, n})
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].n != pairs[j].n {
			return pairs[i].n > pairs[j].n
		}
		return naturalLess(pairs[i].k, pairs[j].k)
	})
	limit := 12
	if len(pairs) < limit {
		limit = len(pairs)
	}
	for _, p := range pairs[:limit] {
		out.Top = append(out.Top, ValueCount{Value: p.k, Count: p.n})
	}

	if numeric && len(values) > 0 {
		out.Num = summarise(values, c)
	}
	return out
}

func summarise(values []float64, c *Column) *NumStats {
	sorted := make([]float64, len(values))
	copy(sorted, values)
	sort.Float64s(sorted)

	n := float64(len(sorted))
	sum := 0.0
	for _, v := range sorted {
		sum += v
	}
	mean := sum / n

	varsum := 0.0
	for _, v := range sorted {
		d := v - mean
		varsum += d * d
	}

	median := sorted[len(sorted)/2]
	if len(sorted)%2 == 0 {
		median = (sorted[len(sorted)/2-1] + sorted[len(sorted)/2]) / 2
	}

	s := &NumStats{
		Min:    sorted[0],
		Max:    sorted[len(sorted)-1],
		Mean:   mean,
		Median: median,
		Sum:    sum,
		StdDev: math.Sqrt(varsum / n),
	}
	s.MinText = formatKey(s.Min, c)
	s.MaxText = formatKey(s.Max, c)

	lo, hi := s.Min, s.Max
	if hi <= lo {
		hi = lo + 1
	}
	s.HistLo, s.HistHi = lo, hi
	s.Hist = make([]int, histBuckets)
	for _, v := range sorted {
		b := int((v - lo) / (hi - lo) * histBuckets)
		if b >= histBuckets {
			b = histBuckets - 1
		}
		if b < 0 {
			b = 0
		}
		s.Hist[b]++
	}
	return s
}

// formatKey turns a sort key back into something a person reads.
func formatKey(v float64, c *Column) string {
	if c.Kind == kindDate {
		layout := c.DateLayout
		if layout == "" {
			layout = "2006-01-02"
		}
		return time.Unix(int64(v), 0).UTC().Format(layout)
	}
	return formatNumber(v)
}

// formatNumber prints a number compactly: whole numbers keep no decimals,
// fractions keep up to four significant ones, and thousands are grouped.
func formatNumber(v float64) string {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return "-"
	}
	abs := math.Abs(v)
	var s string
	switch {
	case abs >= 1e15 || (abs > 0 && abs < 1e-4):
		return strconv.FormatFloat(v, 'g', 6, 64)
	case v == math.Trunc(v):
		s = strconv.FormatFloat(v, 'f', 0, 64)
	case abs >= 100:
		s = strconv.FormatFloat(v, 'f', 2, 64)
	case abs >= 1:
		s = strconv.FormatFloat(v, 'f', 3, 64)
	default:
		s = strconv.FormatFloat(v, 'f', 4, 64)
	}
	if strings.Contains(s, ".") {
		s = strings.TrimRight(s, "0")
		s = strings.TrimSuffix(s, ".")
	}
	return groupDigits(s)
}

func groupDigits(s string) string {
	sign := ""
	if strings.HasPrefix(s, "-") {
		sign, s = "-", s[1:]
	}
	intPart, frac := s, ""
	if i := strings.IndexByte(s, '.'); i >= 0 {
		intPart, frac = s[:i], s[i:]
	}
	if len(intPart) <= 4 {
		return sign + intPart + frac
	}
	var b strings.Builder
	for i, c := range intPart {
		if i > 0 && (len(intPart)-i)%3 == 0 {
			b.WriteRune(thinSpace) // groups digits without committing to a locale
		}
		b.WriteRune(c)
	}
	return sign + b.String() + frac
}
