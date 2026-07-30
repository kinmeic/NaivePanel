package web

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/kinmeic/NaivePanel/internal/sysd"
)

// handleLogs shows panel operation audit records. Service journals live on
// their respective service pages; legacy links are redirected there.
func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	service := r.URL.Query().Get("service")
	lines := logLines(r)
	switch service {
	case "caddy":
		http.Redirect(w, r, s.Cfg.BasePath+"/caddy?lines="+strconv.Itoa(lines), http.StatusSeeOther)
		return
	case "bypasscore":
		http.Redirect(w, r, s.Cfg.BasePath+"/bypasscore?lines="+strconv.Itoa(lines), http.StatusSeeOther)
		return
	}
	journal, err := sysd.Log("naivepanel", 1000)
	content := ""
	if err == nil {
		content = filterOperationLogs(journal, lines)
	}
	data := map[string]any{
		"Service": "operations",
		"Lines":   lines,
		"Content": content,
	}
	if err != nil {
		data["Error"] = err.Error()
	}
	s.render(w, r, "logs", "日志", data)
}

func logLines(r *http.Request) int {
	lines, _ := strconv.Atoi(r.URL.Query().Get("lines"))
	if lines != 100 && lines != 500 && lines != 1000 {
		return 200
	}
	return lines
}

func readServiceLog(r *http.Request, unit string) (lines int, content, errorText string) {
	lines = logLines(r)
	var err error
	content, err = sysd.Log(unit, lines)
	if err != nil {
		errorText = err.Error()
	}
	return
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
