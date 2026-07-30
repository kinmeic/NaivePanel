package web

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/kinmeic/NaivePanel/internal/sysd"
)

// logServices is the whitelist of services whose logs the panel may read.
// The map value is the systemd unit name.
var logServices = map[string]string{
	"caddy":      "caddy",
	"bypasscore": "bypasscore",
}

// handleLogs shows panel operations or the recent journal of a managed service.
func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	service := r.URL.Query().Get("service")
	if service != "operations" {
		if _, ok := logServices[service]; !ok {
			service = "operations"
		}
	}
	lines, _ := strconv.Atoi(r.URL.Query().Get("lines"))
	if lines != 100 && lines != 500 && lines != 1000 {
		lines = 200
	}
	var content string
	var err error
	if service == "operations" {
		var journal string
		journal, err = sysd.Log("naivepanel", 1000)
		if err == nil {
			content = filterOperationLogs(journal, lines)
		}
	} else {
		content, err = sysd.Log(logServices[service], lines)
	}
	data := map[string]any{
		"Service": service,
		"Lines":   lines,
		"Content": content,
	}
	if err != nil {
		data["Error"] = err.Error()
	}
	s.render(w, r, "logs", "日志", data)
}

func filterOperationLogs(journal string, lines int) string {
	var matched []string
	for _, line := range strings.Split(journal, "\n") {
		if strings.Contains(line, operationLogMarker) {
			matched = append(matched, line)
		}
	}
	if len(matched) == 0 {
		return "（暂无操作日志）"
	}
	if len(matched) > lines {
		matched = matched[len(matched)-lines:]
	}
	return strings.Join(matched, "\n") + "\n"
}
