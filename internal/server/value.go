package server

import (
	"math"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// Column kinds, as reported to the viewer.
const (
	kindText   = "text"
	kindInt    = "int"
	kindNumber = "number"
	kindBool   = "bool"
	kindDate   = "date"
)

// currencyRunes are stripped from the ends of a value before it is read as a
// number, so a column of prices still sorts and averages numerically.
const currencyRunes = "$€£¥₺₽₩₴¢"

// currencyWords covers the few suffixes common enough to be worth knowing.
var currencyWords = []string{"zł", "PLN", "EUR", "USD", "GBP", "TRY", "CHF", "SEK", "NOK", "DKK", "CZK", "HUF", "RON", "TL"}

func isSpace(r rune) bool {
	// Includes the non-breaking and thin spaces that spreadsheets use as
	// digit-group separators.
	return r == ' ' || r == '\t' || r == '\u00a0' || r == '\u202f' || r == '\u2009'
}

func trimValue(s string) string {
	return strings.TrimFunc(s, func(r rune) bool { return isSpace(r) || r == '\r' || r == '\n' })
}

// parseNumber reads a cell as a number. commaDecimal selects the continental
// convention (1.234,56) over the anglophone one (1,234.56); the caller decides
// which fits a column by trying both and keeping whichever explains more of
// the data.
//
// Accepted along the way: a leading or trailing currency symbol, a percent
// sign, accounting negatives in parentheses, and thin/non-breaking spaces as
// digit-group separators.
func parseNumber(s string, commaDecimal bool) (float64, bool) {
	s = trimValue(s)
	if s == "" {
		return 0, false
	}

	neg := false
	if len(s) > 2 && s[0] == '(' && s[len(s)-1] == ')' {
		neg = true
		s = trimValue(s[1 : len(s)-1])
	}

	percent := false
	if strings.HasSuffix(s, "%") {
		percent = true
		s = trimValue(strings.TrimSuffix(s, "%"))
	}

	s = stripCurrency(s)
	if s == "" {
		return 0, false
	}

	// A real minus sign (U+2212) turns up in data exported from spreadsheets.
	if strings.HasPrefix(s, "\u2212") {
		neg = !neg
		s = trimValue(s[len("\u2212"):])
	}
	if s == "" {
		return 0, false
	}
	switch s[0] {
	case '+':
		s = trimValue(s[1:])
	case '-':
		neg = !neg
		s = trimValue(s[1:])
	}
	if s == "" {
		return 0, false
	}

	decimal, group := '.', ','
	if commaDecimal {
		decimal, group = ',', '.'
	}

	// Split off the fractional part first. Doing it this way is what keeps a
	// continental date out of the numbers: 05.01.2024 has dots where a number
	// would have digit groups, and 01 and 2024 are not groups of three.
	intPart, frac := s, ""
	if i := strings.IndexRune(s, decimal); i >= 0 {
		intPart, frac = s[:i], s[i+len(string(decimal)):]
		if frac == "" || strings.ContainsRune(frac, decimal) {
			return 0, false
		}
		for _, r := range frac {
			if r < '0' || r > '9' {
				return 0, false
			}
		}
	}

	segments := splitGroups(intPart, group)
	digits := ""
	for i, seg := range segments {
		if seg == "" {
			// ".5" is a number; "1..5" and "1,,234" are not.
			if len(segments) == 1 && frac != "" {
				continue
			}
			return 0, false
		}
		for _, r := range seg {
			if r < '0' || r > '9' {
				return 0, false
			}
		}
		if len(segments) > 1 {
			if i == 0 {
				if len(seg) > 3 {
					return 0, false
				}
			} else if len(seg) != 3 {
				return 0, false
			}
		}
		digits += seg
	}
	if digits == "" && frac == "" {
		return 0, false
	}
	if digits == "" {
		digits = "0"
	}
	if frac != "" {
		digits += "." + frac
	}

	v, err := strconv.ParseFloat(digits, 64)
	if err != nil {
		return 0, false
	}
	if neg {
		v = -v
	}
	if percent {
		v /= 100
	}
	return v, true
}

// splitGroups cuts an integer part at its digit-group separators: the
// separator for the convention in play, plus the spaces and apostrophes that
// stand in for it in Swiss, French and Nordic exports.
func splitGroups(s string, group rune) []string {
	out := []string{""}
	for _, r := range s {
		if r == group || isSpace(r) || r == '\'' {
			out = append(out, "")
			continue
		}
		out[len(out)-1] += string(r)
	}
	return out
}

// isIntegerText reports whether a cell is a whole number written without a
// decimal part, which is what separates an "int" column from a "number" one.
func isIntegerText(s string) bool {
	s = trimValue(s)
	if s == "" {
		return false
	}
	if strings.ContainsAny(s, ".,%") {
		return false
	}
	if s[0] == '+' || s[0] == '-' {
		s = s[1:]
	}
	if s == "" || len(s) > 18 {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func stripCurrency(s string) string {
	for {
		before := s
		s = trimValue(s)
		if s == "" {
			return s
		}
		r := []rune(s)
		if strings.ContainsRune(currencyRunes, r[0]) {
			s = string(r[1:])
		} else if strings.ContainsRune(currencyRunes, r[len(r)-1]) {
			s = string(r[:len(r)-1])
		} else {
			for _, w := range currencyWords {
				if len(s) > len(w) && strings.EqualFold(s[len(s)-len(w):], w) {
					s = s[:len(s)-len(w)]
					break
				}
			}
		}
		if s == before {
			return s
		}
	}
}

// dateLayouts are tried in order when inferring a date column. Whichever
// explains the most values wins, which is how DD/MM and MM/DD are told apart
// without asking.
var dateLayouts = []string{
	"2006-01-02",
	"2006-01-02 15:04:05",
	"2006-01-02T15:04:05Z07:00",
	"2006-01-02T15:04:05",
	"2006-01-02 15:04",
	"2006/01/02",
	"02.01.2006",
	"02.01.2006 15:04:05",
	"02.01.2006 15:04",
	"02/01/2006",
	"01/02/2006",
	"02/01/2006 15:04",
	"01/02/2006 15:04",
	"02-01-2006",
	"2 January 2006",
	"January 2, 2006",
	"2 Jan 2006",
	"Jan 2, 2006",
}

func parseDateWith(s, layout string) (time.Time, bool) {
	s = trimValue(s)
	if s == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(layout, s)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// boolWords is deliberately narrow: 1 and 0 stay integers, because a column of
// counts that happens to hold only ones and zeroes is not a yes/no column.
var boolWords = map[string]bool{
	"true": true, "false": false,
	"yes": true, "no": false,
	"y": true, "n": false,
	"t": true, "f": false,
}

func parseBool(s string) (bool, bool) {
	v, ok := boolWords[strings.ToLower(trimValue(s))]
	return v, ok
}

// naturalLess orders text the way a person reading a spreadsheet expects:
// case-insensitively, with runs of digits compared by value so item9 comes
// before item10.
func naturalLess(a, b string) bool {
	return naturalCompare(a, b) < 0
}

func naturalCompare(a, b string) int {
	ar, br := []rune(a), []rune(b)
	i, j := 0, 0
	for i < len(ar) && j < len(br) {
		ca, cb := ar[i], br[j]
		if unicode.IsDigit(ca) && unicode.IsDigit(cb) {
			si, sj := i, j
			for i < len(ar) && unicode.IsDigit(ar[i]) {
				i++
			}
			for j < len(br) && unicode.IsDigit(br[j]) {
				j++
			}
			na := strings.TrimLeft(string(ar[si:i]), "0")
			nb := strings.TrimLeft(string(br[sj:j]), "0")
			if len(na) != len(nb) {
				if len(na) < len(nb) {
					return -1
				}
				return 1
			}
			if c := strings.Compare(na, nb); c != 0 {
				return c
			}
			continue
		}
		la, lb := unicode.ToLower(ca), unicode.ToLower(cb)
		if la != lb {
			if la < lb {
				return -1
			}
			return 1
		}
		i++
		j++
	}
	switch {
	case len(ar)-i < len(br)-j:
		return -1
	case len(ar)-i > len(br)-j:
		return 1
	}
	return strings.Compare(a, b)
}

// nan is the sort key used for cells that hold no comparable value.
var nan = math.NaN()
