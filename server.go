package main

import (
	"bytes"
	"embed"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static
var staticFS embed.FS

var tmpl = template.Must(template.New("").Funcs(template.FuncMap{
	"short": func(sha string) string { return sha[:8] },
}).ParseFS(templateFS, "templates/*.html"))

type Server struct {
	shadow *Shadow
	boot   string // identifies this process; pages reload when it changes
	cache  Cache

	mu   sync.Mutex
	subs map[chan string]struct{}
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
		default: // slow client: drop, it will catch up on its next /snapshots
		}
	}
}

func (sv *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /static/", http.FileServerFS(staticFS))
	mux.HandleFunc("GET /{$}", sv.index)
	mux.HandleFunc("GET /snapshots", sv.snapshots)
	mux.HandleFunc("GET /diff/{spec}", sv.diff)
	mux.HandleFunc("GET /events", sv.events)
	return mux
}

func (sv *Server) index(w http.ResponseWriter, r *http.Request) {
	entries, err := sv.shadow.Log("")
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	sv.render(w, "index.html", map[string]any{
		"Dir":     sv.shadow.WorkTree,
		"Boot":    sv.boot,
		"Entries": entries,
		"CSS":     template.CSS(StyleCSS()),
	})
}

func (sv *Server) snapshots(w http.ResponseWriter, r *http.Request) {
	entries, err := sv.shadow.Log(r.URL.Query().Get("after"))
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	sv.render(w, "snapshots.html", entries)
}

func (sv *Server) diff(w http.ResponseWriter, r *http.Request) {
	spec := r.PathValue("spec")
	if html, ok := sv.cache.Get(spec); ok {
		w.Write([]byte(html))
		return
	}
	from, to, _ := strings.Cut(spec, "..")
	if to == "" {
		from, to = "", from
	}
	unified, err := sv.shadow.Diff(from, to)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	files, err := RenderDiff(unified)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, "diff.html", map[string]any{"Spec": spec, "Files": files}); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	sv.cache.Put(spec, template.HTML(buf.String()))
	w.Write(buf.Bytes())
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
