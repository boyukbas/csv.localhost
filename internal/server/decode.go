package server

import (
	"bytes"
	"math"
	"strings"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"
)

// Encoding names accepted as an override and reported back to the viewer.
const (
	encUTF8    = "utf-8"
	encUTF8BOM = "utf-8-bom"
	encUTF16LE = "utf-16le"
	encUTF16BE = "utf-16be"
)

// encodingChoices is what the viewer offers in its encoding menu.
var encodingChoices = []string{
	"auto", encUTF8, "windows-1252", "windows-1250", "windows-1254",
	"iso-8859-2", encUTF16LE, encUTF16BE,
}

// decodeText turns raw file bytes into UTF-8 text. With want empty the
// encoding is detected: byte-order marks first, then valid UTF-8, and finally
// Windows-1252 as the safest single-byte fallback. Excel on a non-UTF-8
// machine writes the local ANSI code page with no marker of any kind, so the
// viewer also lets the reader pick one explicitly.
func decodeText(raw []byte, want string) (text string, encoding string) {
	want = strings.ToLower(strings.TrimSpace(want))

	switch want {
	case "", "auto":
		switch {
		case bytes.HasPrefix(raw, []byte{0xEF, 0xBB, 0xBF}):
			return string(raw[3:]), encUTF8BOM
		case bytes.HasPrefix(raw, []byte{0xFF, 0xFE}):
			return decodeUTF16(raw[2:], false), encUTF16LE
		case bytes.HasPrefix(raw, []byte{0xFE, 0xFF}):
			return decodeUTF16(raw[2:], true), encUTF16BE
		case looksLikeUTF16(raw, false):
			return decodeUTF16(raw, false), encUTF16LE
		case looksLikeUTF16(raw, true):
			return decodeUTF16(raw, true), encUTF16BE
		case utf8.Valid(raw):
			return string(raw), encUTF8
		default:
			name, tbl := guessSingleByte(raw)
			return decodeSingleByte(raw, tbl), name
		}

	case encUTF8, encUTF8BOM, "utf8":
		return string(bytes.TrimPrefix(raw, []byte{0xEF, 0xBB, 0xBF})), encUTF8

	case encUTF16LE, "utf-16":
		return decodeUTF16(bytes.TrimPrefix(raw, []byte{0xFF, 0xFE}), false), encUTF16LE

	case encUTF16BE:
		return decodeUTF16(bytes.TrimPrefix(raw, []byte{0xFE, 0xFF}), true), encUTF16BE
	}

	if tbl, ok := singleByteTables[want]; ok {
		return decodeSingleByte(raw, tbl), want
	}
	// Unknown name: fall back to detection rather than refusing to open.
	return decodeText(raw, "")
}

// codePageOrder is the order candidates are tried in, and the tie-break when
// two of them explain the bytes equally well.
var codePageOrder = []string{"windows-1252", "windows-1250", "windows-1254", "iso-8859-2"}

// guessSingleByte picks a code page for a file that carries no marker of its
// own. Text read through the wrong 8-bit table turns accented letters into
// symbols - "maslo" becomes "mas\u00b3o" - so the table that yields the most
// letters is almost always the right one. Where two tables agree (1252 and
// 1254 differ in six positions, all letters in both) the reader can still
// choose, and the viewer says which one it used.
func guessSingleByte(raw []byte) (string, *[128]rune) {
	sample := raw
	if len(sample) > 256<<10 {
		sample = sample[:256<<10]
	}

	var high []byte
	for _, c := range sample {
		if c >= 0x80 {
			high = append(high, c)
		}
	}
	if len(high) == 0 {
		return "windows-1252", &tbl_cp1252
	}

	bestName, bestScore := "windows-1252", math.Inf(-1)
	bestTable := &tbl_cp1252
	for _, name := range codePageOrder {
		tbl := singleByteTables[name]
		if tbl == nil {
			continue
		}
		score := 0.0
		for _, c := range high {
			r := tbl[c-0x80]
			switch {
			case r == 0xFFFD:
				score -= 3 // an undefined byte rules the table out
			case unicode.IsLetter(r):
				score++
			case unicode.IsSpace(r) || unicode.IsPunct(r):
				score += 0.1
			}
		}
		if score > bestScore {
			bestName, bestScore, bestTable = name, score, tbl
		}
	}
	return bestName, bestTable
}

// decodeSingleByte expands an 8-bit code page into UTF-8.
func decodeSingleByte(raw []byte, tbl *[128]rune) string {
	var b strings.Builder
	b.Grow(len(raw) + len(raw)/4)
	for _, c := range raw {
		if c < 0x80 {
			b.WriteByte(c)
			continue
		}
		b.WriteRune(tbl[c-0x80])
	}
	return b.String()
}

func decodeUTF16(raw []byte, bigEndian bool) string {
	if len(raw) < 2 {
		return ""
	}
	units := make([]uint16, 0, len(raw)/2)
	for i := 0; i+1 < len(raw); i += 2 {
		if bigEndian {
			units = append(units, uint16(raw[i])<<8|uint16(raw[i+1]))
		} else {
			units = append(units, uint16(raw[i+1])<<8|uint16(raw[i]))
		}
	}
	return string(utf16.Decode(units))
}

// looksLikeUTF16 spots BOM-less UTF-16 by the run of zero bytes that Latin
// text produces in one half of every code unit.
func looksLikeUTF16(raw []byte, bigEndian bool) bool {
	n := len(raw)
	if n < 32 || n%2 != 0 {
		return false
	}
	if n > 4096 {
		n = 4096
	}
	zeros := 0
	pairs := 0
	for i := 0; i+1 < n; i += 2 {
		pairs++
		b := raw[i]
		if bigEndian {
			// A big-endian ASCII character is 0x00 then the byte.
			if b == 0 && raw[i+1] != 0 {
				zeros++
			}
		} else {
			if raw[i+1] == 0 && b != 0 {
				zeros++
			}
		}
	}
	return pairs > 0 && float64(zeros)/float64(pairs) > 0.7
}
