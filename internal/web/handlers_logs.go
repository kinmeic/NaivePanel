package web

import (
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/kinmeic/NaivePanel/internal/sysd"
)

type operationLogEntry struct {
	Time        string
	User        string
	Method      string
	Path        string
	Status      string
	StatusClass string
	Duration    string
}

type logsPageData struct {
	Lines   int
	Entries []operationLogEntry
	Error   string
}

var (
	operationFieldsPattern = regexp.MustCompile(regexp.QuoteMeta(operationLogMarker) +
		`\s+user=("(?:\\.|[^"\\])*")\s+method=("(?:\\.|[^"\\])*")\s+path=("(?:\\.|[^"\\])*")\s+status=([0-9]+)\s+duration_ms=([0-9]+)`)
	shortISOTimePattern = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2})T(\d{2}:\d{2}:\d{2})`)
	goLogTimePattern    = regexp.MustCompile(`(\d{4})/(\d{2})/(\d{2})\s+(\d{2}:\d{2}:\d{2})`)
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
	entries := []operationLogEntry(nil)
	if err == nil {
		entries = parseOperationLogs(journal, lines)
	}
	data := logsPageData{Lines: lines, Entries: entries}
	if err != nil {
		data.Error = err.Error()
	}
	s.render(w, r, "logs", "操作日志", data)
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
	matched := filterOperationLogLines(journal, lines)
	if len(matched) == 0 {
		return "（暂无操作日志）"
	}
	return strings.Join(matched, "\n") + "\n"
}

func filterOperationLogLines(journal string, lines int) []string {
	var matched []string
	for _, line := range strings.Split(journal, "\n") {
		if strings.Contains(line, operationLogMarker) {
			matched = append(matched, line)
		}
	}
	if len(matched) > lines {
		matched = matched[len(matched)-lines:]
	}
	return matched
}

func parseOperationLogs(journal string, lines int) []operationLogEntry {
	rawLines := filterOperationLogLines(journal, lines)
	entries := make([]operationLogEntry, 0, len(rawLines))
	for _, line := range rawLines {
		entries = append(entries, parseOperationLogLine(line))
	}
	return entries
}

func parseOperationLogLine(line string) operationLogEntry {
	entry := operationLogEntry{
		Time:     operationLogTime(line),
		User:     "—",
		Method:   "—",
		Status:   "—",
		Duration: "—",
	}
	match := operationFieldsPattern.FindStringSubmatch(line)
	if match == nil {
		if marker := strings.Index(line, operationLogMarker); marker >= 0 {
			entry.Path = strings.TrimSpace(line[marker+len(operationLogMarker):])
		}
		if entry.Path == "" {
			entry.Path = "无法解析的旧格式日志"
		}
		return entry
	}

	entry.User = unquoteLogField(match[1])
	entry.Method = unquoteLogField(match[2])
	entry.Path = unquoteLogField(match[3])
	entry.Status = match[4]
	entry.Duration = match[5] + " ms"
	status, _ := strconv.Atoi(match[4])
	switch {
	case status >= 500:
		entry.StatusClass = "bad"
	case status >= 400:
		entry.StatusClass = "warn"
	case status >= 200 && status < 400:
		entry.StatusClass = "ok"
	}
	return entry
}

func unquoteLogField(value string) string {
	unquoted, err := strconv.Unquote(value)
	if err != nil {
		return value
	}
	return unquoted
}

func operationLogTime(line string) string {
	if match := shortISOTimePattern.FindStringSubmatch(line); match != nil {
		return match[1] + " " + match[2]
	}
	matches := goLogTimePattern.FindAllStringSubmatch(line, -1)
	if len(matches) == 0 {
		return "—"
	}
	match := matches[len(matches)-1]
	return match[1] + "-" + match[2] + "-" + match[3] + " " + match[4]
}
