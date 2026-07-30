package web

import (
	"net/http"
	"strconv"

	"github.com/kinmeic/NaivePanel/internal/sysd"
)

// logServices is the whitelist of services whose logs the panel may read.
// The map value is the systemd unit name.
var logServices = map[string]string{
	"caddy":      "caddy",
	"bypasscore": "bypasscore",
}

// handleLogs shows the recent journal of Caddy or BypassCore.
func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	service := r.URL.Query().Get("service")
	if _, ok := logServices[service]; !ok {
		service = "caddy"
	}
	lines, _ := strconv.Atoi(r.URL.Query().Get("lines"))
	if lines != 100 && lines != 500 && lines != 1000 {
		lines = 200
	}
	content, err := sysd.Log(logServices[service], lines)
	data := map[string]any{
		"Service": service,
		"Lines":   lines,
		"Content": content,
	}
	if err != nil {
		data["Error"] = err.Error()
	}
	s.render(w, r, "logs", "服务日志", data)
}
