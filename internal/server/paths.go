package server

import (
	"fmt"
	"net/url"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

// delimitedExts are rendered as a table rather than served as raw bytes.
var delimitedExts = map[string]bool{
	".csv": true,
	".tsv": true,
	".tab": true,
	".psv": true,
	".dsv": true,
}

func isDelimited(p string) bool {
	return delimitedExts[strings.ToLower(filepath.Ext(p))]
}

// defaultDelimiterFor returns the delimiter implied by an extension, if any.
// Detection is skipped for these, because a .tsv containing more commas than
// tabs is still a .tsv.
func defaultDelimiterFor(p string) string {
	switch strings.ToLower(filepath.Ext(p)) {
	case ".tsv", ".tab":
		return "\t"
	case ".psv":
		return "|"
	}
	return ""
}

// NormalizeDelimiter turns a human spelling of a delimiter into the single
// character used internally. An unrecognised value means "detect".
func NormalizeDelimiter(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "":
		return ""
	case "tab", "\\t", "\t":
		return "\t"
	case "comma", ",":
		return ","
	case "semicolon", "semi", ";":
		return ";"
	case "pipe", "bar", "|":
		return "|"
	case "colon", ":":
		return ":"
	case "space":
		return " "
	}
	r := []rune(s)
	if len(r) == 1 {
		return string(r[0])
	}
	return ""
}

// delimiterName is the human label shown in the viewer.
func delimiterName(d rune) string {
	switch d {
	case '\t':
		return "Tab"
	case ',':
		return "Comma"
	case ';':
		return "Semicolon"
	case '|':
		return "Pipe"
	case ':':
		return "Colon"
	case ' ':
		return "Space"
	}
	return string(d)
}

var driveRe = regexp.MustCompile(`^[A-Za-z]:/`)

// pathToURLPath encodes an absolute filesystem path as the tail of a /view/
// URL. Keeping the real directory structure in the URL means relative links
// between files in the same folder resolve correctly with no rewriting.
func pathToURLPath(abs string) string {
	s := filepath.ToSlash(abs)
	if strings.HasPrefix(s, "//") { // UNC share: //server/share/...
		return "unc/" + strings.TrimPrefix(s, "//")
	}
	return strings.TrimPrefix(s, "/")
}

// urlPathToPath is the inverse of pathToURLPath.
func urlPathToPath(rest string) (string, error) {
	rest = strings.TrimPrefix(rest, "/")
	if rest == "" {
		return "", fmt.Errorf("empty path")
	}
	if strings.HasPrefix(rest, "unc/") {
		return filepath.FromSlash("//" + strings.TrimPrefix(rest, "unc/")), nil
	}
	if runtime.GOOS == "windows" {
		if !driveRe.MatchString(rest) {
			return "", fmt.Errorf("expected a drive-qualified path, got %q", rest)
		}
		return filepath.FromSlash(rest), nil
	}
	return filepath.FromSlash("/" + rest), nil
}

// viewURL builds the full browser URL for a file.
func viewURL(host string, port int, abs string, opts ParseOptions) string {
	u := &url.URL{Path: "/view/" + pathToURLPath(abs)}
	q := url.Values{}
	if opts.Delimiter != "" {
		q.Set("d", opts.Delimiter)
	}
	if opts.Header != "" && opts.Header != "auto" {
		q.Set("header", opts.Header)
	}
	if opts.Encoding != "" {
		q.Set("enc", opts.Encoding)
	}
	u.RawQuery = q.Encode()
	return displayOrigin(host, port) + u.String()
}

// withinRoot reports whether target sits inside root (or is root itself).
func withinRoot(root, target string) bool {
	if runtime.GOOS == "windows" {
		// Windows paths are case-insensitive, but filepath.Rel is not, so a
		// link that differs only in case would otherwise be refused.
		root = strings.ToLower(root)
		target = strings.ToLower(target)
	}
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	return !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != ".."
}
