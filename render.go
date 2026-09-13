package main

import (
	"bytes"
	"html/template"
	"path"
	"strings"
	"sync"
	"unicode/utf8"

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
func hunkRows(h *diff.Hunk, hl func(string, span) template.HTML, f *File) []Row {
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
				var d, a span
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
			rows = append(rows, Row{Kind: "add", NewNum: newN, New: hl(l[1:], span{})})
			newN++
			i++
		default:
			text := strings.TrimPrefix(l, " ")
			c := hl(text, span{})
			rows = append(rows, Row{Kind: "ctx", OldNum: oldN, NewNum: newN, Old: c, New: c})
			oldN++
			newN++
			i++
		}
	}
	return rows
}

// span is a byte range [A,B) to emphasise within a line; zero = none.
type span struct{ A, B int }

// inlineDiff finds the changed middle of two lines by stripping their common
// prefix and suffix, so a single added space shows up as a highlighted block.
func inlineDiff(old, new string) (span, span) {
	p := 0
	for p < len(old) && p < len(new) && old[p] == new[p] {
		p++
	}
	for p > 0 && p < len(old) && !utf8.RuneStart(old[p]) {
		p--
	}
	s := 0
	for s < len(old)-p && s < len(new)-p && old[len(old)-1-s] == new[len(new)-1-s] {
		s++
	}
	for s > 0 && !utf8.RuneStart(old[len(old)-s]) {
		s--
	}
	if p == 0 && s == 0 {
		return span{}, span{} // whole line differs: marking everything is noise
	}
	return span{p, len(old) - s}, span{p, len(new) - s}
}

// highlighter returns a per-line syntax highlighter for the given file name,
// emitting chroma spans and wrapping the emphasised range in <mark>.
// ponytail: line-by-line highlighting mis-colours multi-line tokens (block
// comments, raw strings); switch to whole-file highlighting if it bothers.
func highlighter(name string) func(string, span) template.HTML {
	lexer := lexers.Match(path.Base(name))
	if lexer == nil {
		lexer = lexers.Fallback
	}
	lexer = chroma.Coalesce(lexer)
	return func(s string, m span) template.HTML {
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
			for _, cut := range []int{m.A, m.B} {
				if cut > off && cut < off+len(tok.Value) {
					emit(&buf, class, tok.Value[:cut-off], off, m)
					tok.Value, off = tok.Value[cut-off:], cut
				}
			}
			emit(&buf, class, tok.Value, off, m)
			off += len(tok.Value)
		}
		return template.HTML(buf.String())
	}
}

func emit(buf *strings.Builder, class, text string, off int, m span) {
	if text == "" {
		return
	}
	if off == m.A && m.A < m.B {
		buf.WriteString("<mark>")
	}
	if class != "" {
		buf.WriteString(`<span class="` + class + `">`)
	}
	buf.WriteString(template.HTMLEscapeString(text))
	if class != "" {
		buf.WriteString("</span>")
	}
	if off+len(text) == m.B && m.A < m.B {
		buf.WriteString("</mark>")
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
