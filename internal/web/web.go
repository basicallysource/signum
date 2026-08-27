// Package web is the whole user interface: server-rendered pages on the
// design language in agent-docs/design.md, plus the small JSON API the
// printer agents post to. The same handler serves the hosted server and the
// desktop app; the desktop is just this bound to localhost over a local
// database.
package web

import (
	"embed"
	"html/template"
	"log/slog"
	"net/http"

	"github.com/basicallysource/signum/internal/blob"
	"github.com/basicallysource/signum/internal/store"
)

// FaceOption is one candidate face the engraver offers, in rank order.
type FaceOption struct {
	// Note is the human answer to "where is this": bed face, top face, wall.
	Note string `json:"note"`
	// Cap is the text height in mm that fits there; Depth the pocket depth.
	Cap   float64 `json:"cap"`
	Depth float64 `json:"depth"`
}

// Engraver is the seam to the geometry: rank the faces of an STL, cut text
// into one of them.
type Engraver interface {
	// Faces returns the ranked candidate faces where the text fits.
	Faces(stl []byte, lines []string) ([]FaceOption, error)
	// Cut engraves the lines into face number `face` and returns the new STL.
	Cut(stl []byte, lines []string, face int) ([]byte, error)
}

// Server carries what the handlers need.
type Server struct {
	Store   *store.DB
	Blobs   blob.Store
	Engrave Engraver
	// Identity is the identity service base URL; empty means this instance
	// runs open with no accounts at all (the desktop app, or a server
	// behind its own front door). Set, every page requires sign-in.
	Identity string
	// BaseURL is where this instance lives publicly, for identity's
	// redirect back. Required when Identity is set.
	BaseURL string
	Logger  *slog.Logger

	verified verifiedTokens
}

//go:embed templates/*.html static/*
var files embed.FS

var templates = template.Must(template.ParseFS(files, "templates/*.html"))

// Handler builds the routes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /{$}", s.home)
	mux.Handle("GET /static/", http.FileServerFS(files))

	mux.HandleFunc("POST /projects", s.createProject)
	mux.HandleFunc("GET /p/{project}", s.projectPage)
	mux.HandleFunc("GET /p/{project}/upload", s.uploadPage)
	mux.HandleFunc("POST /p/{project}/upload", s.upload)

	mux.HandleFunc("GET /u/{uid}", s.partPage)
	mux.HandleFunc("POST /u/{uid}/engrave", s.reEngrave)
	mux.HandleFunc("GET /u/{uid}/file/{file}", s.download)
	mux.HandleFunc("POST /lookup", s.lookup)

	mux.HandleFunc("GET /printers", s.printersPage)

	mux.HandleFunc("POST /api/jobs", s.recordJob)
	mux.HandleFunc("GET /auth/callback", s.authCallback)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	})

	if s.Identity != "" {
		return s.requireSession(mux)
	}
	return mux
}

func (s *Server) logger() *slog.Logger {
	if s.Logger != nil {
		return s.Logger
	}
	return slog.Default()
}

// render writes a page, or a plain error when the template itself fails.
// Every page learns who is looking at it, for the header.
func (s *Server) render(w http.ResponseWriter, r *http.Request, name string, data any) {
	if page, ok := data.(map[string]any); ok {
		if _, exists := page["Viewer"]; !exists {
			if viewer, ok := viewerFrom(r.Context()); ok {
				page["Viewer"] = viewer
			}
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := templates.ExecuteTemplate(w, name, data); err != nil {
		s.logger().Error("render", "template", name, "error", err)
	}
}

func (s *Server) fail(w http.ResponseWriter, r *http.Request, status int, message string, err error) {
	if err != nil {
		s.logger().Error(message, "error", err)
	}
	w.WriteHeader(status)
	s.render(w, r, "error.html", map[string]any{"Message": message})
}
