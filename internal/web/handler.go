package web

import (
	"bytes"
	"embed"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"time"
)

//go:embed static/index.html
var staticFS embed.FS

// TemplateData holds the values injected into the landing page.
type TemplateData struct {
	Hostname   string
	DefaultTTL string
	MaxTTL     string
}

// Handler serves the embedded landing page.
type Handler struct {
	rendered []byte
	logger   *slog.Logger
}

// NewHandler creates a new web handler that renders the landing page
// with the given hostname and TTL values.
func NewHandler(hostname string, defaultTTL, maxTTL time.Duration, logger *slog.Logger) (*Handler, error) {
	tmplBytes, err := staticFS.ReadFile("static/index.html")
	if err != nil {
		return nil, err
	}

	tmpl, err := template.New("index").Parse(string(tmplBytes))
	if err != nil {
		return nil, err
	}

	data := TemplateData{
		Hostname:   hostname,
		DefaultTTL: formatDuration(defaultTTL),
		MaxTTL:     formatDuration(maxTTL),
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return nil, err
	}

	return &Handler{
		rendered: buf.Bytes(),
		logger:   logger,
	}, nil
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=300")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(h.rendered)
}

func formatDuration(d time.Duration) string {
	switch {
	case d >= 24*time.Hour && d%(24*time.Hour) == 0:
		days := int(d / (24 * time.Hour))
		if days == 1 {
			return "1 day"
		}
		return fmt.Sprintf("%d days", days)
	case d >= time.Hour && d%time.Hour == 0:
		return fmt.Sprintf("%dh", int(d.Hours()))
	case d >= time.Minute && d%time.Minute == 0:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	default:
		return d.String()
	}
}
