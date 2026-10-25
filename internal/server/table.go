package server

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	// maxFileBytes caps what will be read into memory at once.
	maxFileBytes = 256 << 20
	// maxRows caps how many data rows are kept. Beyond this the viewer shows
	// a truncation notice rather than exhausting memory.
	maxRows = 500_000
	// sniffBytes is how much of the file is examined to guess the dialect.
	sniffBytes = 512 << 10
	// typeSample is how many rows are inspected when inferring column types.
	typeSample = 5000
)

// ParseOptions are the reader's overrides for the guesses this package makes.
// The zero value means "work it out".
type ParseOptions struct {
	Delimiter string // "" = detect
	Header    string // "" or "auto" | "yes" | "no"
	Encoding  string // "" = detect
}

func (o ParseOptions) normalised() ParseOptions {
	o.Delimiter = NormalizeDelimiter(o.Delimiter)
	switch o.Header {
	case "yes", "no":
	default:
		o.Header = "auto"
	}
	o.Encoding = strings.ToLower(strings.TrimSpace(o.Encoding))
	if o.Encoding == "auto" {
		o.Encoding = ""
	}
	return o
}

func (o ParseOptions) key() string {
	o = o.normalised()
	return o.Delimiter + "|" + o.Header + "|" + o.Encoding
}

// Column describes one field of the table.
type Column struct {
	Name         string `json:"name"`
	Kind         string `json:"kind"`
	Align        string `json:"align"`
	Width        int    `json:"width"`
	DateLayout   string `json:"dateLayout,omitempty"`
	CommaDecimal bool   `json:"commaDecimal,omitempty"`

	once sync.Once
	keys []float64 // lazily built numeric sort keys, NaN where absent
}

// Table is a parsed delimited file held in memory.
type Table struct {
	Path      string
	Name      string
	ModTime   time.Time
	Size      int64
	Opts      ParseOptions
	Delim     rune
	Encoding  string
	HasHeader bool
	Columns   []*Column
	Rows      [][]string
	Truncated bool
	Warnings  []string

	mu      sync.Mutex
	views   map[string][]int32
	viewLRU []string
}

// sepDirectiveRe matches the "sep=;" line Excel writes at the top of a CSV to
// tell itself which delimiter it used.
var sepDirectiveRe = regexp.MustCompile(`^sep=(.)\r?\n`)

// LoadTable reads and parses a delimited file.
func LoadTable(path string, opts ParseOptions) (*Table, error) {
	opts = opts.normalised()

	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		return nil, errors.New("that is a folder, not a file")
	}
	if info.Size() > maxFileBytes {
		return nil, fmt.Errorf("file is %.0f MB; the viewer reads up to %d MB",
			float64(info.Size())/(1<<20), maxFileBytes>>20)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	text, encoding := decodeText(raw, opts.Encoding)

	t := &Table{
		Path:     path,
		Name:     filepath.Base(path),
		ModTime:  info.ModTime(),
		Size:     info.Size(),
		Opts:     opts,
		Encoding: encoding,
		views:    map[string][]int32{},
	}

	if opts.Encoding == "" && (strings.HasPrefix(encoding, "windows-") || strings.HasPrefix(encoding, "iso-")) {
		t.Warnings = append(t.Warnings, "This file carries no encoding marker, so the text was read as "+
			encoding+". If accented characters look wrong, pick another encoding from the options menu.")
	}

	// Excel's own "sep=" hint beats any guess we could make.
	if m := sepDirectiveRe.FindStringSubmatch(text); m != nil {
		if opts.Delimiter == "" {
			opts.Delimiter = m[1]
		}
		text = text[len(m[0]):]
		t.Warnings = append(t.Warnings, "Dropped the leading \"sep=\" line written by Excel.")
	}

	switch {
	case opts.Delimiter != "":
		t.Delim, _ = utf8.DecodeRuneInString(opts.Delimiter)
	case defaultDelimiterFor(path) != "":
		t.Delim, _ = utf8.DecodeRuneInString(defaultDelimiterFor(path))
	default:
		t.Delim = sniffDelimiter(text)
	}

	records, warn, err := readRecords(text, t.Delim)
	if err != nil {
		return nil, err
	}
	t.Warnings = append(t.Warnings, warn...)
	if len(records) > maxRows+1 {
		records = records[:maxRows+1]
		t.Truncated = true
		t.Warnings = append(t.Warnings,
			fmt.Sprintf("Showing the first %s rows; the file has more.", commas(maxRows)))
	}

	width := 0
	for _, rec := range records {
		if len(rec) > width {
			width = len(rec)
		}
	}
	if width == 0 {
		width = 1
	}
	for i, rec := range records { // square off ragged rows
		if len(rec) < width {
			padded := make([]string, width)
			copy(padded, rec)
			records[i] = padded
		}
	}

	switch opts.Header {
	case "yes":
		t.HasHeader = true
	case "no":
		t.HasHeader = false
	default:
		t.HasHeader = detectHeader(records)
	}

	var head []string
	if t.HasHeader && len(records) > 0 {
		head = records[0]
		t.Rows = records[1:]
	} else {
		t.Rows = records
	}

	t.Columns = buildColumns(head, t.Rows, width, t.Delim)
	return t, nil
}

// readRecords parses the whole text with the given delimiter, tolerating the
// quoting mistakes that real exports are full of.
func readRecords(text string, delim rune) ([][]string, []string, error) {
	r := csv.NewReader(strings.NewReader(text))
	r.Comma = delim
	r.FieldsPerRecord = -1
	r.LazyQuotes = true
	r.ReuseRecord = false

	var (
		out      [][]string
		warnings []string
		bad      int
	)
	for {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			var pe *csv.ParseError
			if errors.As(err, &pe) {
				bad++
				if bad <= 3 {
					warnings = append(warnings, fmt.Sprintf("Line %d: %s", pe.Line, pe.Err))
				}
				continue
			}
			return nil, warnings, err
		}
		out = append(out, rec)
	}
	if bad > 3 {
		warnings = append(warnings, fmt.Sprintf("...and %d more malformed lines were skipped.", bad-3))
	}
	// A trailing newline yields nothing, but a file of blank lines can leave
	// empty single-field records behind; drop them from the tail.
	for len(out) > 0 && len(out[len(out)-1]) == 1 && trimValue(out[len(out)-1][0]) == "" {
		out = out[:len(out)-1]
	}
	return out, warnings, nil
}

// sniffDelimiter picks the separator that yields the most consistent table.
func sniffDelimiter(text string) rune {
	prefix := text
	if len(prefix) > sniffBytes {
		prefix = prefix[:sniffBytes]
		if i := strings.LastIndexByte(prefix, '\n'); i > 0 {
			prefix = prefix[:i]
		}
	}

	best := ','
	bestScore := -1.0
	for _, cand := range []rune{',', ';', '\t', '|', ':'} {
		score := dialectScore(prefix, cand)
		if score > bestScore {
			best, bestScore = cand, score
		}
	}
	return best
}

// dialectScore rewards a delimiter that splits every line into the same number
// of fields, and rewards more fields as a tie-break.
func dialectScore(prefix string, delim rune) float64 {
	r := csv.NewReader(strings.NewReader(prefix))
	r.Comma = delim
	r.FieldsPerRecord = -1
	r.LazyQuotes = true

	counts := map[int]int{}
	total := 0
	for total < 64 {
		rec, err := r.Read()
		if err != nil {
			break
		}
		counts[len(rec)]++
		total++
	}
	if total == 0 {
		return 0
	}
	modal, modalN := 0, 0
	for n, c := range counts {
		if c > modalN || (c == modalN && n > modal) {
			modal, modalN = n, c
		}
	}
	if modal < 2 {
		return 0
	}
	consistency := float64(modalN) / float64(total)
	return consistency*1000 + math.Min(float64(modal), 60)
}

// detectHeader decides whether the first record names the columns. The signal
// that settles most files: a column whose first cell is text while the rest of
// it is numeric or dated.
func detectHeader(records [][]string) bool {
	if len(records) == 0 {
		return false
	}
	first := records[0]
	if len(records) == 1 {
		return true // nothing to compare against; names are the safer reading
	}
	body := records[1:]
	if len(body) > 200 {
		body = body[:200]
	}

	strong := 0
	weak := 0
	for c := range first {
		head := trimValue(first[c])
		if head == "" {
			continue
		}
		if _, ok := parseNumber(head, false); ok {
			return false // a number in the first row is data, not a name
		}
		typed, seen := 0, 0
		for _, rec := range body {
			if c >= len(rec) {
				continue
			}
			v := trimValue(rec[c])
			if v == "" {
				continue
			}
			seen++
			if _, ok := parseNumber(v, false); ok {
				typed++
				continue
			}
			if _, ok := parseNumber(v, true); ok {
				typed++
				continue
			}
			for _, layout := range dateLayouts {
				if _, ok := parseDateWith(v, layout); ok {
					typed++
					break
				}
			}
		}
		if seen >= 3 && float64(typed)/float64(seen) > 0.8 {
			strong++
		} else if seen > 0 {
			weak++
		}
	}
	if strong > 0 {
		return true
	}
	// No typed columns to lean on: names are still the likelier reading when
	// the first row is complete and free of repeats.
	seen := map[string]bool{}
	for _, cell := range first {
		v := strings.ToLower(trimValue(cell))
		if v == "" || seen[v] {
			return weak == 0
		}
		seen[v] = true
	}
	return true
}

// buildColumns names the columns and infers what each one holds.
func buildColumns(head []string, rows [][]string, width int, delim rune) []*Column {
	cols := make([]*Column, width)
	used := map[string]int{}

	for i := 0; i < width; i++ {
		name := ""
		if i < len(head) {
			name = trimValue(head[i])
		}
		if name == "" {
			name = spreadsheetName(i)
		}
		if n, clash := used[strings.ToLower(name)]; clash {
			used[strings.ToLower(name)] = n + 1
			name = fmt.Sprintf("%s (%d)", name, n+1)
		} else {
			used[strings.ToLower(name)] = 1
		}
		cols[i] = &Column{Name: name}
	}

	sample := rows
	if len(sample) > typeSample {
		sample = sample[:typeSample]
	}

	for i, col := range cols {
		inferColumn(col, sample, i, delim)
		col.Width = suggestWidth(col, rows, i)
		switch col.Kind {
		case kindInt, kindNumber:
			col.Align = "right"
		case kindBool:
			col.Align = "center"
		default:
			col.Align = "left"
		}
	}
	return cols
}

// inferColumn works out a column's kind from a sample of its values.
func inferColumn(col *Column, sample [][]string, idx int, delim rune) {
	var (
		nonEmpty                                int
		ints, dotNums, commaNums, bools, blanks int
	)
	layoutHits := map[string]int{}
	distinctBool := map[string]bool{}

	for _, rec := range sample {
		if idx >= len(rec) {
			continue
		}
		v := trimValue(rec[idx])
		if v == "" {
			blanks++
			continue
		}
		nonEmpty++
		if isIntegerText(v) {
			ints++
		}
		if _, ok := parseNumber(v, false); ok {
			dotNums++
		}
		if _, ok := parseNumber(v, true); ok {
			commaNums++
		}
		if _, ok := parseBool(v); ok {
			bools++
			distinctBool[strings.ToLower(v)] = true
		}
		for _, layout := range dateLayouts {
			if _, ok := parseDateWith(v, layout); ok {
				layoutHits[layout]++
				break
			}
		}
	}
	_ = blanks

	if nonEmpty == 0 {
		col.Kind = kindText
		return
	}

	// A yes/no column is only that if nothing else is mixed in.
	if bools == nonEmpty && len(distinctBool) <= 4 {
		col.Kind = kindBool
		return
	}

	if ints == nonEmpty {
		col.Kind = kindInt
		return
	}

	if dotNums == nonEmpty || commaNums == nonEmpty {
		col.Kind = kindNumber
		// Prefer whichever convention explains more values; on a tie, a
		// semicolon-delimited file is continental and a comma-delimited one
		// is not.
		switch {
		case commaNums > dotNums:
			col.CommaDecimal = true
		case dotNums > commaNums:
			col.CommaDecimal = false
		default:
			col.CommaDecimal = delim == ';'
		}
		return
	}

	bestLayout, bestHits := "", 0
	for layout, n := range layoutHits {
		if n > bestHits || (n == bestHits && layout < bestLayout) {
			bestLayout, bestHits = layout, n
		}
	}
	if bestHits == nonEmpty && bestHits > 0 {
		col.Kind = kindDate
		col.DateLayout = bestLayout
		return
	}

	col.Kind = kindText
}

// suggestWidth gives the browser a starting column width in pixels, based on
// the longest value in a sample rather than on the whole file.
func suggestWidth(col *Column, rows [][]string, idx int) int {
	longest := utf8.RuneCountInString(col.Name) + 3 // room for the sort arrow
	limit := len(rows)
	if limit > 400 {
		limit = 400
	}
	for _, rec := range rows[:limit] {
		if idx >= len(rec) {
			continue
		}
		if n := utf8.RuneCountInString(rec[idx]); n > longest {
			longest = n
			if longest > 60 {
				break
			}
		}
	}
	px := longest*8 + 26
	if px < 84 {
		px = 84
	}
	if px > 420 {
		px = 420
	}
	return px
}

// spreadsheetName labels an unnamed column the way a spreadsheet would: A, B,
// ... Z, AA, AB.
func spreadsheetName(i int) string {
	name := ""
	for i >= 0 {
		name = string(rune('A'+i%26)) + name
		i = i/26 - 1
	}
	return name
}

// sortKeys returns the numeric sort key for every row of a column: the value
// itself for numbers, seconds since the epoch for dates, 0/1 for booleans, and
// NaN wherever there is nothing to compare.
func (t *Table) sortKeys(idx int) []float64 {
	col := t.Columns[idx]
	col.once.Do(func() {
		keys := make([]float64, len(t.Rows))
		for i, rec := range t.Rows {
			keys[i] = nan
			if idx >= len(rec) {
				continue
			}
			v := trimValue(rec[idx])
			if v == "" {
				continue
			}
			switch col.Kind {
			case kindInt, kindNumber:
				if f, ok := parseNumber(v, col.CommaDecimal); ok {
					keys[i] = f
				}
			case kindDate:
				if ts, ok := parseDateWith(v, col.DateLayout); ok {
					keys[i] = float64(ts.Unix())
				}
			case kindBool:
				if b, ok := parseBool(v); ok {
					if b {
						keys[i] = 1
					} else {
						keys[i] = 0
					}
				}
			}
		}
		col.keys = keys
	})
	return col.keys
}

func (t *Table) cell(row, col int) string {
	if row < 0 || row >= len(t.Rows) {
		return ""
	}
	rec := t.Rows[row]
	if col < 0 || col >= len(rec) {
		return ""
	}
	return rec[col]
}

// thinSpace groups digits in text meant for people to read.
const thinSpace = '\u2009'

// commas formats a count with thousands separators for use in messages.
func commas(n int) string {
	s := fmt.Sprintf("%d", n)
	if len(s) <= 3 {
		return s
	}
	var out []byte
	for i, c := range []byte(s) {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, string(thinSpace)...)
		}
		out = append(out, c)
	}
	return string(out)
}
