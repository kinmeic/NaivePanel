package fail2ban

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

type BanRecord struct {
	Jail       string
	IP         string
	BannedAt   time.Time
	UnbannedAt time.Time
	State      string
	Current    bool
	observedAt time.Time
}

type Snapshot struct {
	Installed    bool
	Active       bool
	Enabled      bool
	Jails        []string
	CurrentCount int
	Records      []BanRecord
	StatusError  string
	HistoryError string
}

func (m *Manager) Snapshot() Snapshot {
	s := Snapshot{Installed: m.Installed()}
	if !s.Installed {
		return s
	}
	s.Active, s.Enabled = m.active("fail2ban"), m.enabled("fail2ban")
	var current map[string][]string
	logPath := m.logPath
	if s.Active {
		out, err := m.client("banned")
		if err == nil {
			current, err = parseBanned(out)
		}
		if err != nil {
			s.StatusError = "读取当前封禁列表失败: " + err.Error()
		}
		if target, err := m.client("get", "logtarget"); err == nil {
			lines := strings.Split(strings.TrimSpace(target), "\n")
			if path := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(lines[len(lines)-1]), "`- ")); filepath.IsAbs(path) {
				logPath = path
			}
		}
	}
	for jail, ips := range current {
		s.Jails = append(s.Jails, jail)
		s.CurrentCount += len(ips)
	}
	sort.Strings(s.Jails)
	events, err := m.readEvents(logPath)
	if err != nil {
		s.HistoryError = err.Error()
	}
	// The web layer paginates all records available within the bounded log
	// window. Do not discard older cycles before the page is selected.
	s.Records = buildRecords(events, current, 0)
	return s
}

// Fail2ban 0.11+ prints Python containers for `banned`. Only decode data;
// never evaluate Python or pass jail/IP values through a shell.
func parseBanned(out string) (map[string][]string, error) {
	var groups []map[string][]string
	parsed := false
	lines := strings.Split(out, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if !strings.HasPrefix(line, "[") {
			continue
		}
		if err := json.Unmarshal([]byte(strings.ReplaceAll(line, "'", `"`)), &groups); err == nil {
			parsed = true
			break
		}
	}
	if !parsed {
		return nil, errors.New("无法识别 fail2ban-client banned 输出")
	}
	result := map[string][]string{}
	for _, group := range groups {
		for jail, ips := range group {
			result[jail] = nil
			for _, ip := range ips {
				addr, err := netip.ParseAddr(ip)
				if err != nil {
					return nil, fmt.Errorf("无法识别封禁地址 %q", ip)
				}
				result[jail] = append(result[jail], addr.Unmap().String())
			}
		}
	}
	return result, nil
}

type banEvent struct {
	jail   string
	ip     string
	action string
	at     time.Time
}

var eventPattern = regexp.MustCompile(`\[([^\]\r\n]+)\]\s+(Restore Ban|Ban|Unban)\s+(\S+)`)
var datePattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}(?:[,\.]\d+)?`)

func parseEvent(message string, fallback time.Time) (banEvent, bool) {
	match := eventPattern.FindStringSubmatch(message)
	if match == nil {
		return banEvent{}, false
	}
	ip, err := netip.ParseAddr(match[3])
	if err != nil {
		return banEvent{}, false
	}
	at := fallback
	if date := datePattern.FindString(message); date != "" {
		if t, err := time.ParseInLocation("2006-01-02 15:04:05.999999999", strings.ReplaceAll(date, ",", "."), time.Local); err == nil {
			at = t
		}
	}
	if at.IsZero() {
		return banEvent{}, false
	}
	return banEvent{jail: match[1], ip: ip.Unmap().String(), action: match[2], at: at}, true
}

func (m *Manager) readEvents(logPath string) ([]banEvent, error) {
	var events []banEvent
	var issues []string
	for _, path := range []string{logPath + ".1", logPath} {
		b, err := readTail(path, 1<<20)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			issues = append(issues, "读取 "+path+" 失败: "+err.Error())
			continue
		}
		for _, line := range strings.Split(string(b), "\n") {
			if event, ok := parseEvent(line, time.Time{}); ok {
				events = append(events, event)
			}
		}
	}
	journal, err := m.run(10*time.Second, "journalctl", "-u", "fail2ban.service", "-n", "2000", "--no-pager", "-o", "json")
	if err != nil {
		issues = append(issues, "读取 fail2ban journal 失败: "+err.Error())
	} else {
		for _, line := range strings.Split(journal, "\n") {
			var entry struct {
				Message   string `json:"MESSAGE"`
				Timestamp string `json:"__REALTIME_TIMESTAMP"`
			}
			if json.Unmarshal([]byte(line), &entry) != nil {
				continue
			}
			micros, err := strconv.ParseInt(entry.Timestamp, 10, 64)
			if err != nil {
				continue
			}
			if event, ok := parseEvent(entry.Message, time.UnixMicro(micros).In(time.Local)); ok {
				events = append(events, event)
			}
		}
	}
	if len(issues) > 0 {
		return events, errors.New(strings.Join(issues, "；"))
	}
	return events, nil
}

// Pair each ban cycle independently, retaining unknown timestamps rather than
// inventing an unban time from the configured bantime. Restores are not new bans.
func buildRecords(events []banEvent, current map[string][]string, limit int) []BanRecord {
	sort.SliceStable(events, func(i, j int) bool { return events[i].at.Before(events[j].at) })
	var records []BanRecord
	open := map[string]int{}
	seen := map[string]bool{}
	for _, e := range events {
		key := e.jail + "\x00" + e.ip
		identity := key + "\x00" + e.action + "\x00" + e.at.Format(time.RFC3339Nano)
		if seen[identity] {
			continue
		}
		seen[identity] = true
		i, pending := open[key]
		switch e.action {
		case "Restore Ban":
			if pending {
				continue
			}
			open[key] = len(records)
			records = append(records, BanRecord{Jail: e.jail, IP: e.ip, State: "状态未知", observedAt: e.at})
		case "Ban":
			open[key] = len(records)
			records = append(records, BanRecord{Jail: e.jail, IP: e.ip, BannedAt: e.at, State: "状态未知", observedAt: e.at})
		case "Unban":
			if !pending {
				i = len(records)
				records = append(records, BanRecord{Jail: e.jail, IP: e.ip})
			}
			records[i].UnbannedAt, records[i].observedAt, records[i].State = e.at, e.at, "已解禁"
			delete(open, key)
		}
	}
	if current != nil {
		for _, i := range open {
			records[i].State = "已不在封禁列表"
		}
		for jail, ips := range current {
			for _, ip := range ips {
				key := jail + "\x00" + ip
				i, ok := open[key]
				if !ok {
					i = len(records)
					records = append(records, BanRecord{Jail: jail, IP: ip})
				}
				records[i].Current, records[i].State = true, "封禁中"
			}
		}
	}
	// Active bans remain visible even when their original log entry has rotated.
	sort.SliceStable(records, func(i, j int) bool {
		if records[i].Current != records[j].Current {
			return records[i].Current
		}
		if records[i].observedAt.Equal(records[j].observedAt) {
			if records[i].Jail != records[j].Jail {
				return records[i].Jail < records[j].Jail
			}
			return records[i].IP < records[j].IP
		}
		return records[i].observedAt.After(records[j].observedAt)
	})
	if limit > 0 && len(records) > limit {
		records = records[:limit]
	}
	return records
}
