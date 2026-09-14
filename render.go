package main

import (
	"bytes"
	"fmt"
	"html/template"
	"path"
	"strings"
	"sync"

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
	Big            bool // collapsed by default: too many changed lines to scan (fixtures, lockfiles, cassettes)
	Rows           []Row
}

var formatter = html.New(html.PreventSurroundingPre(true))

// bigFile is the changed-line count above which a file starts collapsed.
// ponytail: fixed threshold; make it a setting if it keeps getting in the way.
const bigFile = 200

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
		f.Big = f.Plus+f.Minus > bigFile
		files = append(files, f)
	}
	return files, nil
}

// hunkRows pairs consecutive -/+ runs into shared rows.
func hunkRows(h *diff.Hunk, hl func(string) template.HTML, f *File) []Row {
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
				if j < len(dels) && j < len(adds) {
					r.Kind = "mod"
				} else if j >= len(dels) {
					r.Kind = "add"
				}
				if j < len(dels) {
					r.OldNum, r.Old = oldN, hl(dels[j])
					oldN++
				}
				if j < len(adds) {
					r.NewNum, r.New = newN, hl(adds[j])
					newN++
				}
				rows = append(rows, r)
			}
		case strings.HasPrefix(l, "+"):
			f.Plus++
			rows = append(rows, Row{Kind: "add", NewNum: newN, New: hl(l[1:])})
			newN++
			i++
		default:
			text := strings.TrimPrefix(l, " ")
			c := hl(text)
			rows = append(rows, Row{Kind: "ctx", OldNum: oldN, NewNum: newN, Old: c, New: c})
			oldN++
			newN++
			i++
		}
	}
	return rows
}

// highlighter returns a per-line syntax highlighter for the given file name.
// ponytail: line-by-line highlighting mis-colours multi-line tokens (block
// comments, raw strings); switch to whole-file highlighting if it bothers.
func highlighter(name string) func(string) template.HTML {
	lexer := lexers.Match(path.Base(name))
	if lexer == nil {
		lexer = lexers.Fallback
	}
	lexer = chroma.Coalesce(lexer)
	return func(s string) template.HTML {
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
			off += len(tok.Value)
			class := ""
			for t := tok.Type; t != 0 && class == ""; t = t.Parent() {
				class = chroma.StandardTypes[t]
			}
			if class != "" {
				buf.WriteString(`<span class="` + class + `">`)
			}
			buf.WriteString(template.HTMLEscapeString(tok.Value))
			if class != "" {
				buf.WriteString("</span>")
			}
		}
		return template.HTML(buf.String())
	}
}

// ThemeCSS returns the stylesheet for a chroma style: the UI palette derived
// from the style's background/foreground, then the token colours.
func ThemeCSS(name string) string {
	st := styles.Get(name) // Fallback when unknown
	bg := st.Get(chroma.Background)
	fg, back := bg.Colour, bg.Background
	if !back.IsSet() {
		back = chroma.MustParseColour("#1e1f22")
	}
	if !fg.IsSet() {
		fg = chroma.MustParseColour("#d4d4d4")
	}
	dir := 1.0 // dark theme: lighten to get panels and lines
	if back.Brightness() > 0.5 {
		dir = -1
	}
	var buf bytes.Buffer
	fmt.Fprintf(&buf, ":root{--bg:%s;--bg2:%s;--line:%s;--fg:%s;--dim:%s}\n",
		back, back.Brighten(0.06*dir), back.Brighten(0.16*dir), fg, fg.Brighten(-0.35*dir))
	_ = formatter.WriteCSS(&buf, st)
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
