package main

import (
	"bytes"
	"html/template"
	"path"
	"strings"
	"sync"
	"unicode"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/formatters/html"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
	"github.com/sourcegraph/go-diff/diff"
)

// Row is one side-by-side line: either side may be empty (no number).
type Row struct {
	Kind           string // "ctx", "add", "del", "mod", "hunk"
	OldNum, NewNum int32
	Old, New       template.HTML
}

type File struct {
	Path, OldPath  string // OldPath set only on rename
	Added, Deleted bool
	Plus, Minus    int
	Rows           []Row
}

var (
	formatter = html.New(html.PreventSurroundingPre(true))
	style     = styles.Get("monokai")
)

// RenderDiff turns a unified diff into side-by-side file blocks.
func RenderDiff(unified string) ([]File, error) {
	fds, err := diff.ParseMultiFileDiff([]byte(unified))
	if err != nil {
		return nil, err
	}
	files := make([]File, 0, len(fds))
	for _, fd := range fds {
		orig, name := strings.TrimPrefix(fd.OrigName, "a/"), strings.TrimPrefix(fd.NewName, "b/")
		f := File{Path: name, Added: fd.OrigName == "/dev/null", Deleted: fd.NewName == "/dev/null"}
		if f.Deleted {
			f.Path = orig
		} else if orig != name && !f.Added {
			f.OldPath = orig
		}
		hl := highlighter(f.Path)
		for _, h := range fd.Hunks {
			f.Rows = append(f.Rows, Row{Kind: "hunk", Old: template.HTML(template.HTMLEscapeString(
				strings.TrimSpace(string(h.Section))))})
			f.Rows = append(f.Rows, hunkRows(h, hl, &f)...)
		}
		files = append(files, f)
	}
	return files, nil
}

// hunkRows pairs consecutive -/+ runs into shared rows.
func hunkRows(h *diff.Hunk, hl func(string, []span) template.HTML, f *File) []Row {
	var rows []Row
	oldN, newN := int32(h.OrigStartLine), int32(h.NewStartLine)
	lines := strings.Split(strings.TrimSuffix(string(h.Body), "\n"), "\n")
	for i := 0; i < len(lines); {
		l := lines[i]
		switch {
		case strings.HasPrefix(l, "\\"): // "\ No newline at end of file"
			i++
		case strings.HasPrefix(l, "-"):
			var dels []string
			for ; i < len(lines) && strings.HasPrefix(lines[i], "-"); i++ {
				dels = append(dels, lines[i][1:])
			}
			var adds []string
			for ; i < len(lines) && (strings.HasPrefix(lines[i], "+") || strings.HasPrefix(lines[i], "\\")); i++ {
				if lines[i][0] == '+' {
					adds = append(adds, lines[i][1:])
				}
			}
			f.Minus += len(dels)
			f.Plus += len(adds)
			for j := 0; j < max(len(dels), len(adds)); j++ {
				r := Row{Kind: "del"}
				var d, a []span
				if j < len(dels) && j < len(adds) {
					r.Kind = "mod"
					d, a = inlineDiff(dels[j], adds[j])
				} else if j >= len(dels) {
					r.Kind = "add"
				}
				if j < len(dels) {
					r.OldNum, r.Old = oldN, hl(dels[j], d)
					oldN++
				}
				if j < len(adds) {
					r.NewNum, r.New = newN, hl(adds[j], a)
					newN++
				}
				rows = append(rows, r)
			}
		case strings.HasPrefix(l, "+"):
			f.Plus++
			rows = append(rows, Row{Kind: "add", NewNum: newN, New: hl(l[1:], nil)})
			newN++
			i++
		default:
			text := strings.TrimPrefix(l, " ")
			c := hl(text, nil)
			rows = append(rows, Row{Kind: "ctx", OldNum: oldN, NewNum: newN, Old: c, New: c})
			oldN++
			newN++
			i++
		}
	}
	return rows
}

// span is a byte range [A,B) to emphasise within a line.
type span struct{ A, B int }

// words splits a line into tokens: runs of letters/digits/_, runs of spaces,
// and single punctuation characters.
func words(s string) []string {
	var out []string
	start := 0
	class := func(r rune) int {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_':
			return 1
		case r == ' ' || r == '\t':
			return 2
		}
		return 0 // punctuation: never merged
	}
	prev := -1
	for i, r := range s {
		c := class(r)
		if i > 0 && (c == 0 || c != prev) {
			out = append(out, s[start:i])
			start = i
		}
		prev = c
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}

// inlineDiff marks the words that differ between two lines (LCS on tokens), so
// an added space or a changed word is highlighted without drowning the line.
// ponytail: O(n*m) LCS; lines with too many tokens are left unmarked.
func inlineDiff(old, new string) (o, n []span) {
	ot, nt := words(old), words(new)
	if len(ot)*len(nt) > 40000 {
		return nil, nil
	}
	dp := make([][]int, len(ot)+1)
	for i := range dp {
		dp[i] = make([]int, len(nt)+1)
	}
	for i := 1; i <= len(ot); i++ {
		for j := 1; j <= len(nt); j++ {
			if ot[i-1] == nt[j-1] {
				dp[i][j] = dp[i-1][j-1] + 1
			} else {
				dp[i][j] = max(dp[i-1][j], dp[i][j-1])
			}
		}
	}
	if dp[len(ot)][len(nt)] == 0 {
		return nil, nil // nothing in common: marking everything is noise
	}
	omark, nmark := make([]bool, len(ot)), make([]bool, len(nt))
	for i, j := len(ot), len(nt); i > 0 || j > 0; {
		switch {
		case i > 0 && j > 0 && ot[i-1] == nt[j-1]:
			i, j = i-1, j-1
		case j == 0 || (i > 0 && dp[i-1][j] >= dp[i][j-1]):
			omark[i-1] = true
			i--
		default:
			nmark[j-1] = true
			j--
		}
	}
	return spans(ot, omark), spans(nt, nmark)
}

// spans converts per-token marks into merged byte ranges.
func spans(toks []string, marked []bool) []span {
	var out []span
	off := 0
	for i, t := range toks {
		if marked[i] {
			if len(out) > 0 && out[len(out)-1].B == off {
				out[len(out)-1].B += len(t)
			} else {
				out = append(out, span{off, off + len(t)})
			}
		}
		off += len(t)
	}
	return out
}

// highlighter returns a per-line syntax highlighter for the given file name,
// emitting chroma spans and wrapping the emphasised ranges in <mark>.
// ponytail: line-by-line highlighting mis-colours multi-line tokens (block
// comments, raw strings); switch to whole-file highlighting if it bothers.
func highlighter(name string) func(string, []span) template.HTML {
	lexer := lexers.Match(path.Base(name))
	if lexer == nil {
		lexer = lexers.Fallback
	}
	lexer = chroma.Coalesce(lexer)
	return func(s string, marks []span) template.HTML {
		it, err := lexer.Tokenise(nil, s)
		if err != nil {
			return template.HTML(template.HTMLEscapeString(s))
		}
		var buf strings.Builder
		off := 0
		for _, tok := range it.Tokens() {
			if off+len(tok.Value) > len(s) { // lexers with EnsureNL append a newline
				tok.Value = tok.Value[:len(s)-off]
			}
			class := ""
			for t := tok.Type; t != 0 && class == ""; t = t.Parent() {
				class = chroma.StandardTypes[t]
			}
			for _, m := range marks {
				for _, cut := range []int{m.A, m.B} {
					if cut > off && cut < off+len(tok.Value) {
						emit(&buf, class, tok.Value[:cut-off], off, marks)
						tok.Value, off = tok.Value[cut-off:], cut
					}
				}
			}
			emit(&buf, class, tok.Value, off, marks)
			off += len(tok.Value)
		}
		return template.HTML(buf.String())
	}
}

func emit(buf *strings.Builder, class, text string, off int, marks []span) {
	if text == "" {
		return
	}
	for _, m := range marks {
		if off == m.A {
			buf.WriteString("<mark>")
		}
	}
	if class != "" {
		buf.WriteString(`<span class="` + class + `">`)
	}
	buf.WriteString(template.HTMLEscapeString(text))
	if class != "" {
		buf.WriteString("</span>")
	}
	for _, m := range marks {
		if off+len(text) == m.B {
			buf.WriteString("</mark>")
		}
	}
}

// StyleCSS is the chroma stylesheet for the chosen style.
func StyleCSS() string {
	var buf bytes.Buffer
	_ = formatter.WriteCSS(&buf, style)
	return buf.String()
}

// Cache is a bounded memo of rendered diff fragments keyed by "from..to".
// ponytail: arbitrary eviction (first key found); LRU if hit rate matters.
type Cache struct {
	mu sync.Mutex
	m  map[string]template.HTML
}

func (c *Cache) Get(key string) (template.HTML, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.m[key]
	return v, ok
}

func (c *Cache) Put(key string, v template.HTML) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = make(map[string]template.HTML)
	}
	if len(c.m) >= 256 {
		for k := range c.m {
			delete(c.m, k)
			break
		}
	}
	c.m[key] = v
}
