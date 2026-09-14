package main

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/alecthomas/chroma/v2/styles"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static
var staticFS embed.FS

var tmpl = template.Must(template.New("").Funcs(template.FuncMap{
	// dict builds a map for passing several values to a sub-template
	"dict": func(kv ...any) map[string]any {
		m := make(map[string]any, len(kv)/2)
		for i := 0; i+1 < len(kv); i += 2 {
			m[kv[i].(string)] = kv[i+1]
		}
		return m
	},
	"tree": Tree,
}).ParseFS(templateFS, "templates/*.html"))

// pageSize is how many snapshots one /feed response carries.
const pageSize = 20

// historyPage is how many commits one /history page carries.
const historyPage = 50

type Server struct {
	shadow *Shadow
	boot   string // identifies this process; pages reload when it changes
	cache  Cache

	mu   sync.Mutex
	subs map[chan string]struct{}
}

// Section is one snapshot in the feed: its timeline entry plus rendered diff.
type Section struct {
	Entry
	Diff template.HTML
}

func NewServer(s *Shadow) *Server {
	return &Server{shadow: s, boot: strconv.FormatInt(time.Now().UnixNano(), 36), subs: make(map[chan string]struct{})}
}

// Connected reports whether at least one browser tab is listening.
func (sv *Server) Connected() bool {
	sv.mu.Lock()
	defer sv.mu.Unlock()
	return len(sv.subs) > 0
}

// Broadcast pushes a new snapshot SHA to every SSE client.
func (sv *Server) Broadcast(sha string) {
	sv.mu.Lock()
	defer sv.mu.Unlock()
	for ch := range sv.subs {
		select {
		case ch <- sha:
		default: // slow client: drop, it will catch up on its next /feed
		}
	}
}

func (sv *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /static/", http.FileServerFS(staticFS))
	mux.HandleFunc("GET /{$}", sv.index)
	mux.HandleFunc("GET /feed", sv.feed)
	mux.HandleFunc("GET /events", sv.events)
	mux.HandleFunc("GET /history", sv.history)
	mux.HandleFunc("PUT /prefs", savePrefs)
	mux.HandleFunc("GET /file", sv.file)
	mux.HandleFunc("GET /theme/{name}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/css")
		w.Header().Set("Cache-Control", "max-age=86400")
		fmt.Fprint(w, ThemeCSS(strings.TrimSuffix(r.PathValue("name"), ".css")))
	})
	return mux
}

func (sv *Server) index(w http.ResponseWriter, r *http.Request) {
	page, err := sv.page("", "", pageSize)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	page["Dir"] = sv.shadow.WorkTree
	page["Name"] = filepath.Base(sv.shadow.WorkTree)
	page["Prefs"] = loadPrefs()
	page["Boot"] = sv.boot
	page["Themes"] = styles.Names()
	sv.render(w, "index.html", page)
}

// feed serves ?after=<sha> (everything newer, for SSE prepend) or
// ?before=<sha> (next page of older snapshots, for infinite scroll).
func (sv *Server) feed(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	n := pageSize
	if q.Get("after") != "" {
		n = 0
	}
	page, err := sv.page(q.Get("after"), q.Get("before"), n)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	sv.render(w, "feed.html", page)
}

// page builds feed.html's data: rendered sections, whether an older page
// exists, and the oldest SHA to ask it from.
func (sv *Server) page(after, before string, n int) (map[string]any, error) {
	entries, err := sv.shadow.Log(after, before, n)
	if err != nil {
		return nil, err
	}
	sections := make([]Section, 0, len(entries))
	for _, e := range entries {
		d, err := sv.diff(e.SHA)
		if err != nil {
			return nil, err
		}
		sections = append(sections, Section{Entry: e, Diff: d})
	}
	page := map[string]any{"Sections": sections, "More": n > 0 && len(entries) == n}
	if len(entries) > 0 {
		page["Last"] = entries[len(entries)-1].SHA
	}
	return page, nil
}

// history serves the sidebar: pending changes, then the project's commits
// grouped by day. ?before=<sha>&day=<label> fetches the next page (infinite
// scroll; the day header is omitted when it continues the previous page);
// ?n=<count> sizes the first page so a refresh keeps what was loaded.
func (sv *Server) history(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	n, _ := strconv.Atoi(q.Get("n"))
	n = max(historyPage, min(n, 5000))
	before := q.Get("before")

	type day struct {
		Label   string
		Commits []Commit
	}
	var days []day
	today := time.Now().Format(time.DateOnly)
	yesterday := time.Now().AddDate(0, 0, -1).Format(time.DateOnly)
	commits := sv.shadow.History(before, n)
	ours := sv.shadow.BranchCommits()
	for i := range commits {
		commits[i].Ours = ours[commits[i].SHA]
	}
	for _, c := range commits {
		d := c.Time.Format(time.DateOnly)
		label := d
		switch d {
		case today:
			label = "Today"
		case yesterday:
			label = "Yesterday"
		}
		if len(days) == 0 || days[len(days)-1].Label != label {
			days = append(days, day{Label: label})
		}
		days[len(days)-1].Commits = append(days[len(days)-1].Commits, c)
	}
	page := map[string]any{"Days": days, "Before": before, "PrevDay": q.Get("day"), "More": len(commits) == n}
	if before == "" {
		page["Status"] = sv.shadow.Status()
		page["Branch"], _ = sv.shadow.ProjectHead()
	}
	if len(days) > 0 {
		last := days[len(days)-1]
		page["Last"], page["LastDay"] = last.Commits[len(last.Commits)-1].SHA, last.Label
	}
	sv.render(w, "history.html", page)
}

// file serves one file's diff: ?rev=<sha>&path=<p>, or rev empty for the
// working tree. Commit diffs are immutable and memoised; working tree is not.
func (sv *Server) file(w http.ResponseWriter, r *http.Request) {
	rev, path := r.URL.Query().Get("rev"), r.URL.Query().Get("path")
	key := rev + ":" + path
	if rev != "" {
		if h, ok := sv.cache.Get(key); ok {
			w.Write([]byte(h))
			return
		}
	}
	unified, err := sv.shadow.FileDiff(rev, path)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	files, err := RenderDiff(unified)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	for i := range files {
		files[i].Big = false // asked for explicitly: never fold
	}
	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, "file.html", map[string]any{"Rev": rev, "Path": path, "Files": files}); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	if rev != "" {
		sv.cache.Put(key, template.HTML(buf.String()))
	}
	w.Write(buf.Bytes())
}

// diff renders (and memoises) the side-by-side HTML of one snapshot.
func (sv *Server) diff(sha string) (template.HTML, error) {
	if h, ok := sv.cache.Get(sha); ok {
		return h, nil
	}
	unified, err := sv.shadow.Diff("", sha)
	if err != nil {
		return "", err
	}
	files, err := RenderDiff(unified)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, "diff.html", files); err != nil {
		return "", err
	}
	h := template.HTML(buf.String())
	sv.cache.Put(sha, h)
	return h, nil
}

func (sv *Server) events(w http.ResponseWriter, r *http.Request) {
	rc := http.NewResponseController(w)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	ch := make(chan string, 16)
	sv.mu.Lock()
	sv.subs[ch] = struct{}{}
	sv.mu.Unlock()
	defer func() {
		sv.mu.Lock()
		delete(sv.subs, ch)
		sv.mu.Unlock()
	}()
	fmt.Fprintf(w, "retry: 500\nevent: boot\ndata: %s\n\n", sv.boot)
	rc.Flush()
	for {
		select {
		case <-r.Context().Done():
			return
		case sha := <-ch:
			fmt.Fprintf(w, "event: snapshot\ndata: %s\n\n", sha)
			if err := rc.Flush(); err != nil {
				return
			}
		}
	}
}

func (sv *Server) render(w http.ResponseWriter, name string, data any) {
	if err := tmpl.ExecuteTemplate(w, name, data); err != nil {
		log.Println("render:", err)
	}
}

// Preferences (theme, font, sidebar…) are a JSON blob owned by the page,
// stored once for all projects and ports in $XDG_CONFIG_HOME/snapwatch/prefs.json.
func prefsPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = "."
	}
	return filepath.Join(dir, "snapwatch", "prefs.json")
}

func loadPrefs() template.JS {
	b, err := os.ReadFile(prefsPath())
	if err != nil || !json.Valid(b) {
		return "{}"
	}
	return template.JS(b)
}

func savePrefs(w http.ResponseWriter, r *http.Request) {
	b, err := io.ReadAll(io.LimitReader(r.Body, 64<<10))
	if err != nil || !json.Valid(b) {
		http.Error(w, "invalid JSON", 400)
		return
	}
	p := prefsPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err == nil {
		err = os.WriteFile(p, b, 0o644)
	}
	if err != nil {
		http.Error(w, err.Error(), 500)
	}
}
