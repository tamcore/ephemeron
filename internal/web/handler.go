package web

import (
	"bytes"
	"embed"
	"fmt"
	"html/template"
	"net/http"
	"time"
)

//go:embed static/index.html
var staticFS embed.FS

// NewHandler returns a handler that serves the landing page
// rendered with the given hostname and TTL values.
func NewHandler(hostname string, defaultTTL, maxTTL time.Duration, version string) (http.Handler, error) {
	tmpl, err := template.ParseFS(staticFS, "static/index.html")
	if err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	err = tmpl.Execute(&buf, struct{ Hostname, DefaultTTL, MaxTTL, Version string }{
		hostname, formatDuration(defaultTTL), formatDuration(maxTTL), version,
	})
	if err != nil {
		return nil, err
	}
	rendered := buf.Bytes()

	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "public, max-age=300")
		_, _ = w.Write(rendered)
	}), nil
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
