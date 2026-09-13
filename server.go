package main

import (
	"bytes"
	"embed"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"strconv"
	"sync"
	"time"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static
var staticFS embed.FS

var tmpl = template.Must(template.ParseFS(templateFS, "templates/*.html"))

// pageSize is how many snapshots one /feed response carries.
const pageSize = 20

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
	return mux
}

func (sv *Server) index(w http.ResponseWriter, r *http.Request) {
	page, err := sv.page("", "", pageSize)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	page["Dir"] = sv.shadow.WorkTree
	page["Boot"] = sv.boot
	page["CSS"] = template.CSS(StyleCSS())
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
