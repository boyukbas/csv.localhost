package server

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, name, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func writeBytes(t *testing.T, name string, body []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, body, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func load(t *testing.T, path string, opts ParseOptions) *Table {
	t.Helper()
	tab, err := LoadTable(path, opts)
	if err != nil {
		t.Fatalf("LoadTable(%s): %v", path, err)
	}
	return tab
}

func TestSniffDelimiter(t *testing.T) {
	cases := []struct {
		name string
		body string
		want rune
	}{
		{"comma", "a,b,c\n1,2,3\n4,5,6\n", ','},
		{"semicolon", "a;b;c\n1;2;3\n4;5;6\n", ';'},
		{"tab", "a\tb\tc\n1\t2\t3\n", '\t'},
		{"pipe", "a|b|c\n1|2|3\n4|5|6\n", '|'},
		{"commas inside semicolon fields", "name;note\nAda;one, two, three\nAlan;four, five\n", ';'},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := sniffDelimiter(c.body); got != c.want {
				t.Fatalf("sniffed %q, want %q", string(got), string(c.want))
			}
		})
	}
}

func TestExtensionWinsOverSniffing(t *testing.T) {
	// A .tsv whose fields contain more commas than tabs is still a .tsv.
	p := write(t, "x.tsv", "name\tnote\nAda\ta,b,c,d\nAlan\te,f,g,h\n")
	tab := load(t, p, ParseOptions{})
	if tab.Delim != '\t' {
		t.Fatalf("delimiter %q, want tab", string(tab.Delim))
	}
	if len(tab.Columns) != 2 {
		t.Fatalf("got %d columns, want 2", len(tab.Columns))
	}
}

func TestSepDirective(t *testing.T) {
	p := write(t, "x.csv", "sep=;\na;b\n1;2\n")
	tab := load(t, p, ParseOptions{})
	if tab.Delim != ';' {
		t.Fatalf("delimiter %q, want ;", string(tab.Delim))
	}
	if tab.Columns[0].Name != "a" {
		t.Fatalf("first column %q, want a", tab.Columns[0].Name)
	}
	if len(tab.Rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(tab.Rows))
	}
}

func TestHeaderDetection(t *testing.T) {
	withHeader := write(t, "h.csv", "city,population\nBerlin,3600000\nLodz,670000\nOslo,700000\n")
	if tab := load(t, withHeader, ParseOptions{}); !tab.HasHeader {
		t.Fatal("expected a header row")
	}

	noHeader := write(t, "n.csv", "Berlin,3600000\nLodz,670000\nOslo,700000\nRiga,600000\n")
	if tab := load(t, noHeader, ParseOptions{}); tab.HasHeader {
		t.Fatal("did not expect a header row")
	}

	forced := load(t, withHeader, ParseOptions{Header: "no"})
	if forced.HasHeader {
		t.Fatal("override to no was ignored")
	}
	if len(forced.Rows) != 4 {
		t.Fatalf("got %d rows, want 4", len(forced.Rows))
	}
	if forced.Columns[0].Name != "A" {
		t.Fatalf("unnamed column is %q, want A", forced.Columns[0].Name)
	}
}

func TestColumnTypes(t *testing.T) {
	p := write(t, "t.csv", strings.Join([]string{
		"id,price,when,ok,label,mixed",
		"1,19.99,2024-01-05,true,alpha,7",
		"2,4.50,2024-02-06,false,beta,seven",
		"3,120.00,2024-03-07,yes,gamma,9",
	}, "\n")+"\n")
	tab := load(t, p, ParseOptions{})

	want := map[string]string{
		"id": kindInt, "price": kindNumber, "when": kindDate,
		"ok": kindBool, "label": kindText, "mixed": kindText,
	}
	for _, c := range tab.Columns {
		if want[c.Name] != c.Kind {
			t.Errorf("column %s: kind %s, want %s", c.Name, c.Kind, want[c.Name])
		}
	}
}

func TestContinentalNumbers(t *testing.T) {
	p := write(t, "eu.csv", "produkt;cena\nchleb;3,50\nmleko;2,75\nser;42,00\n")
	tab := load(t, p, ParseOptions{})
	if tab.Delim != ';' {
		t.Fatalf("delimiter %q, want ;", string(tab.Delim))
	}
	price := tab.Columns[1]
	if price.Kind != kindNumber {
		t.Fatalf("price kind %s, want number", price.Kind)
	}
	if !price.CommaDecimal {
		t.Fatal("expected the comma to be read as a decimal point")
	}
	keys := tab.sortKeys(1)
	if math.Abs(keys[0]-3.5) > 1e-9 {
		t.Fatalf("first price parsed as %v, want 3.5", keys[0])
	}
}

func TestParseNumberForms(t *testing.T) {
	cases := []struct {
		in     string
		comma  bool
		want   float64
		wantOK bool
	}{
		{"42", false, 42, true},
		{"-7.5", false, -7.5, true},
		{"1,234.56", false, 1234.56, true},
		{"1.234,56", true, 1234.56, true},
		{"1 234,56", true, 1234.56, true},
		{"$1,200", false, 1200, true},
		{"12,50 zł", true, 12.5, true},
		{"(250)", false, -250, true},
		{"15%", false, 0.15, true},
		{"−3", false, -3, true},
		{"", false, 0, false},
		{"n/a", false, 0, false},
		{"1.2.3", false, 0, false},
		{"12-34", false, 0, false},
		{".5", false, 0.5, true},
		{"12 345 678", true, 12345678, true},
		{"1'234.50", false, 1234.5, true},
		// A continental date is not a number, whichever convention is in play.
		{"05.01.2024", true, 0, false},
		{"05.01.2024", false, 0, false},
		{"1.23", true, 0, false}, // groups must be three digits
		{"1,23", false, 0, false},
		{"1,2345", false, 0, false},
	}
	for _, c := range cases {
		got, ok := parseNumber(c.in, c.comma)
		if ok != c.wantOK || (ok && math.Abs(got-c.want) > 1e-9) {
			t.Errorf("parseNumber(%q, comma=%v) = %v, %v; want %v, %v", c.in, c.comma, got, ok, c.want, c.wantOK)
		}
	}
}

func TestContinentalDatesStayDates(t *testing.T) {
	// The case that gives naive parsers trouble: a semicolon file whose dates
	// use dots, next to a price column whose decimals use commas.
	p := write(t, "pl.csv", "produkt;cena;data\nchleb;3,50;05.01.2024\nmleko;2,75;06.01.2024\nser;42,00;07.01.2024\n")
	tab := load(t, p, ParseOptions{})
	kinds := map[string]string{}
	for _, c := range tab.Columns {
		kinds[c.Name] = c.Kind
	}
	if kinds["data"] != kindDate {
		t.Fatalf("date column read as %q, want date", kinds["data"])
	}
	if kinds["cena"] != kindNumber {
		t.Fatalf("price column read as %q, want number", kinds["cena"])
	}
	if tab.Columns[2].DateLayout != "02.01.2006" {
		t.Fatalf("date layout %q", tab.Columns[2].DateLayout)
	}
	// Sorting has to run through the dates, not through the text.
	idx := tab.view("", 2, "desc")
	if got := tab.cell(int(idx[0]), 2); got != "07.01.2024" {
		t.Fatalf("newest date sorted first gave %q", got)
	}
}

func TestRaggedRowsArePadded(t *testing.T) {
	p := write(t, "r.csv", "a,b,c\n1,2\n3,4,5,6\n")
	tab := load(t, p, ParseOptions{})
	if len(tab.Columns) != 4 {
		t.Fatalf("got %d columns, want 4", len(tab.Columns))
	}
	for i, row := range tab.Rows {
		if len(row) != 4 {
			t.Fatalf("row %d has %d fields, want 4", i, len(row))
		}
	}
}

func TestQuotedFieldsAndNewlines(t *testing.T) {
	p := write(t, "q.csv", "name,note\n\"Ada, Countess\",\"line one\nline two\"\nAlan,plain\n")
	tab := load(t, p, ParseOptions{})
	if got := tab.cell(0, 0); got != "Ada, Countess" {
		t.Fatalf("cell = %q", got)
	}
	if got := tab.cell(0, 1); !strings.Contains(got, "\nline two") {
		t.Fatalf("embedded newline lost: %q", got)
	}
	if len(tab.Rows) != 2 {
		t.Fatalf("got %d rows, want 2", len(tab.Rows))
	}
}

func TestSortingAndBlanksLast(t *testing.T) {
	p := write(t, "s.csv", "name,score\nitem10,5\nitem9,\nitem2,30\n")
	tab := load(t, p, ParseOptions{})

	asc := tab.view("", 0, "asc")
	var names []string
	for _, r := range asc {
		names = append(names, tab.cell(int(r), 0))
	}
	want := []string{"item2", "item9", "item10"}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("natural sort gave %v, want %v", names, want)
		}
	}

	for _, dir := range []string{"asc", "desc"} {
		idx := tab.view("", 1, dir)
		last := tab.cell(int(idx[len(idx)-1]), 1)
		if last != "" {
			t.Fatalf("%s: blank cell should sort last, got %q", dir, last)
		}
	}
}

func TestFiltering(t *testing.T) {
	p := write(t, "f.csv", strings.Join([]string{
		"city,country,population",
		"Berlin,Germany,3600000",
		"Bern,Switzerland,134000",
		"Lodz,Poland,670000",
	}, "\n")+"\n")
	tab := load(t, p, ParseOptions{})

	if got := len(tab.view("ber", -1, "asc")); got != 2 {
		t.Fatalf("free-text search matched %d rows, want 2", got)
	}
	if got := len(tab.view("country:poland", -1, "asc")); got != 1 {
		t.Fatalf("column search matched %d rows, want 1", got)
	}
	if got := len(tab.view("population>500000", -1, "asc")); got != 2 {
		t.Fatalf("numeric comparison matched %d rows, want 2", got)
	}
	if got := len(tab.view("-germany", -1, "asc")); got != 2 {
		t.Fatalf("negated search matched %d rows, want 2", got)
	}
	if got := len(tab.view("city:ber population<200000", -1, "asc")); got != 1 {
		t.Fatalf("combined clauses matched %d rows, want 1", got)
	}
}

func TestStats(t *testing.T) {
	p := write(t, "st.csv", "k,v\na,10\nb,20\na,30\nc,\n")
	tab := load(t, p, ParseOptions{})
	all := tab.view("", -1, "asc")

	text := tab.Stats(0, all)
	if text.Distinct != 3 || text.Filled != 4 {
		t.Fatalf("text stats: distinct=%d filled=%d", text.Distinct, text.Filled)
	}
	if text.Top[0].Value != "a" || text.Top[0].Count != 2 {
		t.Fatalf("most common value = %+v", text.Top[0])
	}

	num := tab.Stats(1, all)
	if num.Empty != 1 || num.Filled != 3 {
		t.Fatalf("numeric stats: empty=%d filled=%d", num.Empty, num.Filled)
	}
	if num.Num == nil || num.Num.Sum != 60 || num.Num.Mean != 20 || num.Num.Median != 20 {
		t.Fatalf("numeric summary = %+v", num.Num)
	}
	if len(num.Num.Hist) != histBuckets {
		t.Fatalf("histogram has %d buckets", len(num.Num.Hist))
	}
}

func TestEncodings(t *testing.T) {
	// Windows-1250: 0x9C is "s with acute" in Polish.
	p := writeBytes(t, "pl.csv", []byte("miasto;kraj\n\x8cwiecie;Polska\n"))
	tab := load(t, p, ParseOptions{Encoding: "windows-1250"})
	if got := tab.cell(0, 0); got != "Świecie" {
		t.Fatalf("cp1250 cell = %q", got)
	}

	utf8bom := writeBytes(t, "bom.csv", append([]byte{0xEF, 0xBB, 0xBF}, []byte("a,b\n1,2\n")...))
	tabB := load(t, utf8bom, ParseOptions{})
	if tabB.Columns[0].Name != "a" {
		t.Fatalf("BOM leaked into the first column name: %q", tabB.Columns[0].Name)
	}

	var utf16 []byte
	utf16 = append(utf16, 0xFF, 0xFE)
	for _, r := range "a,b\n1,2\n" {
		utf16 = append(utf16, byte(r), 0)
	}
	tabU := load(t, writeBytes(t, "u16.csv", utf16), ParseOptions{})
	if tabU.Encoding != encUTF16LE || tabU.cell(0, 1) != "2" {
		t.Fatalf("utf-16 decode: encoding=%s cell=%q", tabU.Encoding, tabU.cell(0, 1))
	}
}

func TestCodePageGuess(t *testing.T) {
	// Polish text in windows-1250: through the 1252 table the same bytes turn
	// into superscripts and inverted punctuation rather than letters.
	polish := []byte("miasto;produkt\nGda\xf1sk;mas\xb3o\nKrak\xf3w;ser \xbf\xf3\xb3ty\n")
	tab := load(t, writeBytes(t, "pl2.csv", polish), ParseOptions{})
	if tab.Encoding != "windows-1250" {
		t.Fatalf("guessed %s, want windows-1250", tab.Encoding)
	}
	if got := tab.cell(1, 1); got != "ser \u017c\u00f3\u0142ty" {
		t.Fatalf("cell = %q", got)
	}
	if len(tab.Warnings) == 0 {
		t.Fatal("expected a notice that the encoding was guessed")
	}

	// Plain western European text still reads as 1252.
	west := []byte("ville,plat\nGen\xe8ve,cr\xeape\nM\xfcnchen,Wei\xdfwurst\n")
	tabW := load(t, writeBytes(t, "fr.csv", west), ParseOptions{})
	if tabW.Encoding != "windows-1252" {
		t.Fatalf("guessed %s, want windows-1252", tabW.Encoding)
	}
	if got := tabW.cell(1, 1); got != "Wei\u00dfwurst" {
		t.Fatalf("cell = %q", got)
	}
}

func TestEmptyAndSingleColumnFiles(t *testing.T) {
	empty := load(t, write(t, "e.csv", ""), ParseOptions{})
	if len(empty.Rows) != 0 || len(empty.Columns) != 1 {
		t.Fatalf("empty file: %d rows, %d columns", len(empty.Rows), len(empty.Columns))
	}

	single := load(t, write(t, "one.csv", "value\n1\n2\n3\n"), ParseOptions{})
	if len(single.Columns) != 1 || len(single.Rows) != 3 {
		t.Fatalf("single column: %d columns, %d rows", len(single.Columns), len(single.Rows))
	}
}

func TestSpreadsheetName(t *testing.T) {
	for i, want := range map[int]string{0: "A", 25: "Z", 26: "AA", 27: "AB", 51: "AZ", 52: "BA"} {
		if got := spreadsheetName(i); got != want {
			t.Errorf("spreadsheetName(%d) = %s, want %s", i, got, want)
		}
	}
}

func TestDuplicateHeadersAreDisambiguated(t *testing.T) {
	tab := load(t, write(t, "d.csv", "id,id,ID\n1,2,3\n"), ParseOptions{})
	seen := map[string]bool{}
	for _, c := range tab.Columns {
		if seen[c.Name] {
			t.Fatalf("duplicate column name %q", c.Name)
		}
		seen[c.Name] = true
	}
}
