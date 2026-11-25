// doclint is the single source of truth for mechanical checks on the design documents under docs.
// It is a separate module so that the app's coverage and mutation testing do not include it.
package main

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

type finding struct {
	severity string
	path     string
	line     int
	message  string
}

type termRule struct {
	re        *regexp.Regexp
	canonical string
}

var (
	boundaryRe = regexp.MustCompile(`[ぁ-んァ-ヿ一-鿿々〆、。「」『』（）！？：【】〈〉][ \t]+[0-9A-Za-z]|[0-9A-Za-z][ \t]+[ぁ-んァ-ヿ一-鿿々〆、。「」『』（）！？：【】〈〉]`)
	// After Japanese text, a colon is flagged even when ASCII comes first; lines without Japanese are not flagged.
	colonEndRe = regexp.MustCompile(`(?:[ぁ-ん一-鿿ァ-ヿ々）」』】〉]|[ぁ-ん一-鿿ァ-ヿ].*[)\]#0-9A-Za-z])[:：][\s\x{3000}]*$`)
	emphasisRe = regexp.MustCompile(`\*\*`)
	// A line of three or more asterisks is a thematic break, not emphasis.
	thematicBreakRe = regexp.MustCompile(`^\s*(?:\*\s*){3,}$`)
	// Japanese deferral words are matched by stem so that polite conjugations (決めます, とします) are caught.
	tbdRe     = regexp.MustCompile(`(?i)\bTBD\b|\bTODOs?\b|後で決め|未定と[すし]|将来検討`)
	headingRe = regexp.MustCompile(`^ {0,3}#{1,6}\s`)
	// A list item may start with a label noun phrase; one space after the label is the separator, not a boundary space.
	listLabelRe = regexp.MustCompile(`^\s*(?:[-*+]|\d+[.)])\s+(\S+) `)
	// A label is a noun phrase, so a first word with punctuation, ending in a particle, or written only in ASCII is ordinary text.
	notLabelRe = regexp.MustCompile(`[、。，．]|[はがをにでとものへや]$|^[!-~]+$`)
	// Any indentation is accepted so that fences nested in list items are recognised.
	fenceRe      = regexp.MustCompile("^\\s*(`{3,}|~{3,})(.*)$")
	inlineCodeRe = regexp.MustCompile("``.*?``|`[^`]*`")
	mdLinkURLRe  = regexp.MustCompile(`\]\([^)]*\)`)
	angleURLRe   = regexp.MustCompile(`<https?://[^>\s]*>`)
	// Japanese text has no space after a URL, so a bare URL ends at the first non-ASCII character; a trailing colon and the `]` left by a link are text.
	bareURLRe = regexp.MustCompile(`https?://[!-\\^-~]*[!-9;-~]`)
	// Matching is case-insensitive; any spelling other than canonical is a finding.
	termRules = []termRule{
		{regexp.MustCompile(`(?i)sqlite3?`), "SQLite"},
		{regexp.MustCompile(`(?i)golang`), "Go"},
		{regexp.MustCompile(`(?i)cloudflare`), "Cloudflare"},
		{regexp.MustCompile(`(?i)litestream`), "Litestream"},
		{regexp.MustCompile(`(?i)postgres(?:ql)?`), "PostgreSQL"},
		{regexp.MustCompile(`(?i)mysql`), "MySQL"},
		{regexp.MustCompile(`(?i)htmx`), "htmx"},
	}
)

func isASCIILetter(b byte) bool { return b|0x20 >= 'a' && b|0x20 <= 'z' }

func isASCIIDigit(b byte) bool { return b >= '0' && b <= '9' }

// termViolations checks word boundaries by hand instead of consuming them in the regexp,
// so two misspellings separated by one character are both reported.
func termViolations(s string, tr termRule) int {
	n := 0
	for _, loc := range tr.re.FindAllStringIndex(s, -1) {
		start, end := loc[0], loc[1]
		if start > 0 && isASCIILetter(s[start-1]) {
			continue
		}
		if end < len(s) && (isASCIILetter(s[end]) || isASCIIDigit(s[end])) {
			continue
		}
		if s[start:end] != tr.canonical {
			n++
		}
	}
	return n
}

// fence is an open fenced code block; it closes only on the same character repeated at least as many times.
type fence struct {
	char byte
	size int
	line int
}

func closesFence(line string, open fence) bool {
	m := fenceRe.FindStringSubmatch(line)
	if m == nil || m[1][0] != open.char || len(m[1]) < open.size {
		return false
	}
	return strings.TrimSpace(m[2]) == ""
}

// opensFence follows CommonMark: a backtick fence whose info string contains a backtick is inline code.
func opensFence(line string) (fence, bool) {
	m := fenceRe.FindStringSubmatch(line)
	if m == nil || (m[1][0] == '`' && strings.Contains(m[2], "`")) {
		return fence{}, false
	}
	return fence{char: m[1][0], size: len(m[1])}, true
}

func lintFile(path string) []finding {
	data, err := os.ReadFile(path) // #nosec G304 -- dev tool; path comes from WalkDir
	if err != nil {
		return []finding{{"Critical", path, 0, "read failed: " + err.Error()}}
	}
	var out []finding
	var open *fence
	n := 0
	for line := range strings.SplitSeq(strings.TrimPrefix(string(data), "\uFEFF"), "\n") {
		n++
		if open != nil {
			if closesFence(line, *open) {
				open = nil
			}
			continue
		}
		if f, ok := opensFence(line); ok {
			f.line = n
			open = &f
			continue
		}
		for _, f := range lintLine(line) {
			f.path, f.line = path, n
			out = append(out, f)
		}
	}
	if open != nil {
		out = append(out, finding{"High", path, open.line, "unclosed code fence"})
	}
	return out
}

func stripQuoted(line string) string {
	// A placeholder keeps the words on both sides of inline code or a URL from being joined, and lets a colon after it be checked.
	s := inlineCodeRe.ReplaceAllString(line, "#")
	s = mdLinkURLRe.ReplaceAllString(s, "]")
	s = angleURLRe.ReplaceAllString(s, "#")
	return bareURLRe.ReplaceAllString(s, "#")
}

func boundaryTarget(stripped string) string {
	m := listLabelRe.FindStringSubmatchIndex(stripped)
	if m == nil {
		return stripped
	}
	if notLabelRe.MatchString(stripped[m[2]:m[3]]) {
		return stripped
	}
	rest := stripped[m[1]:]
	// Only one space separates the label; any further blank is a boundary space in the text.
	if strings.TrimLeft(rest, " \t") != rest {
		return stripped
	}
	return rest
}

func lintLine(line string) []finding {
	// Inline code and URLs are quotations and identifiers, so no rule applies to them.
	stripped := stripQuoted(line)
	var out []finding
	if emphasisRe.MatchString(stripped) && !thematicBreakRe.MatchString(stripped) {
		out = append(out, finding{severity: "High", message: "do not use double-asterisk emphasis"})
	}
	if colonEndRe.MatchString(stripped) {
		out = append(out, finding{severity: "Medium", message: "do not end a Japanese sentence with a colon"})
	}
	if tbdRe.MatchString(stripped) {
		out = append(out, finding{severity: "Critical", message: "do not leave deferral markers (TBD/TODO/後で決める/未定とする/将来検討)"})
	}
	if body := boundaryTarget(stripped); !headingRe.MatchString(line) && boundaryRe.MatchString(body) {
		out = append(out, finding{severity: "High", message: "no half-width space between Japanese and ASCII: " + boundaryRe.FindString(body)})
	}
	for _, tr := range termRules {
		for range termViolations(stripped, tr) {
			out = append(out, finding{severity: "High", message: "term: use " + tr.canonical})
		}
	}
	return out
}

// run lints every .md file under root and returns the process exit code.
func run(root string, w io.Writer) int {
	var all []finding
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(path, ".md") {
			all = append(all, lintFile(path)...)
		}
		return nil
	})
	if err != nil {
		return write(w, fmt.Appendf(nil, "walk error: %v\n", err), 2)
	}
	counts := map[string]int{}
	var buf []byte
	for _, f := range all {
		counts[f.severity]++
		buf = fmt.Appendf(buf, "[%s] %s:%d %s\n", f.severity, f.path, f.line, f.message)
	}
	buf = fmt.Appendf(buf, "Critical %d / High %d / Medium %d / Low %d\n",
		counts["Critical"], counts["High"], counts["Medium"], counts["Low"])
	if counts["Critical"] > 0 || counts["High"] > 0 || counts["Medium"] > 0 {
		return write(w, buf, 1)
	}
	return write(w, buf, 0)
}

// write returns 2 when the report cannot be written, so a broken pipe never looks like a clean run.
func write(w io.Writer, b []byte, code int) int {
	if _, err := w.Write(b); err != nil {
		return 2
	}
	return code
}

func rootArg(args []string) string {
	if len(args) > 1 {
		return args[1]
	}
	return "docs"
}

func main() {
	os.Exit(run(rootArg(os.Args), os.Stdout))
}
