// Package cronmgr manages NaivePanel-owned cron jobs without interpolating
// user scripts into a crontab command line.
package cronmgr

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	maxNameChars   = 80
	maxScriptBytes = 64 << 10
	maxTasks       = 128
	maxStateBytes  = 20 << 20
)

// Paths describes the files owned by the manager.
type Paths struct {
	StateFile string
	ScriptDir string
	CronFile  string
	LogFile   string
}

// DefaultPaths derives private state from the configuration location. Linux
// always uses the system cron and log locations, including with a custom
// -config path; non-Linux development keeps artifacts local.
func DefaultPaths(configPath string) Paths {
	base := filepath.Dir(configPath)
	p := Paths{
		StateFile: filepath.Join(base, "cron-tasks.json"),
		ScriptDir: filepath.Join(base, "cron-scripts"),
		CronFile:  filepath.Join(base, "cron.d-naivepanel"),
		LogFile:   filepath.Join(base, "cron.log"),
	}
	if runtime.GOOS == "linux" {
		p.CronFile = "/etc/cron.d/naivepanel"
		p.LogFile = "/var/log/naivepanel-cron.log"
	}
	return p
}

// Task is one user-managed scheduled script.
type Task struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Schedule  string    `json:"schedule"`
	Script    string    `json:"script"`
	Enabled   bool      `json:"enabled"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Manager serializes task changes and mirrors them to scripts and a cron file.
type Manager struct {
	mu      sync.Mutex
	paths   Paths
	tasks   []Task
	now     func() time.Time
	loadErr error
}

// Paths returns a copy of the manager's filesystem layout.
func (m *Manager) Paths() Paths {
	return m.paths
}

// New loads existing state. Invalid task state disables this optional feature
// without preventing the rest of the panel from starting.
func New(paths Paths) (*Manager, error) {
	if err := validatePaths(paths); err != nil {
		return nil, err
	}
	m := &Manager{paths: paths, now: time.Now}
	file, err := os.Open(paths.StateFile)
	if errors.Is(err, os.ErrNotExist) {
		return m, nil
	}
	if err != nil {
		m.loadErr = fmt.Errorf("读取计划任务状态: %w", err)
		return m, nil
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxStateBytes+1))
	if err != nil {
		m.loadErr = fmt.Errorf("读取计划任务状态: %w", err)
		return m, nil
	}
	if len(data) > maxStateBytes {
		m.loadErr = fmt.Errorf("计划任务状态超过 %d MiB 上限", maxStateBytes>>20)
		return m, nil
	}
	if err := json.Unmarshal(data, &m.tasks); err != nil {
		m.tasks = nil
		m.loadErr = fmt.Errorf("解析计划任务状态: %w", err)
		return m, nil
	}
	if len(m.tasks) > maxTasks {
		m.tasks = nil
		m.loadErr = fmt.Errorf("计划任务数量超过 %d 个上限", maxTasks)
		return m, nil
	}
	seen := make(map[string]bool, len(m.tasks))
	for i := range m.tasks {
		if err := validateTask(m.tasks[i]); err != nil {
			m.tasks = nil
			m.loadErr = fmt.Errorf("计划任务 %d 无效: %w", i+1, err)
			return m, nil
		}
		if seen[m.tasks[i].ID] {
			m.tasks = nil
			m.loadErr = fmt.Errorf("计划任务 ID 重复: %s", m.tasks[i].ID)
			return m, nil
		}
		seen[m.tasks[i].ID] = true
	}
	return m, nil
}

// LoadError reports why task mutations are disabled, if state could not be
// loaded safely.
func (m *Manager) LoadError() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.loadErr
}

func (m *Manager) readyLocked() error {
	if m.loadErr != nil {
		return fmt.Errorf("计划任务状态不可用: %w", m.loadErr)
	}
	return nil
}

func validatePaths(p Paths) error {
	for name, path := range map[string]string{
		"状态文件":    p.StateFile,
		"脚本目录":    p.ScriptDir,
		"Cron 文件": p.CronFile,
		"日志文件":    p.LogFile,
	} {
		if !filepath.IsAbs(path) || strings.ContainsAny(path, "\x00\r\n\t ") {
			return fmt.Errorf("%s必须是无空白或控制字符的绝对路径", name)
		}
	}
	return nil
}

func validateTask(t Task) error {
	if len(t.ID) != 32 {
		return errors.New("任务 ID 无效")
	}
	if _, err := hex.DecodeString(t.ID); err != nil {
		return errors.New("任务 ID 无效")
	}
	name := strings.TrimSpace(t.Name)
	if name == "" || !utf8.ValidString(name) || utf8.RuneCountInString(name) > maxNameChars ||
		strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return fmt.Errorf("名称必须为 1–%d 个字符且不含控制字符", maxNameChars)
	}
	if err := ValidateSchedule(t.Schedule); err != nil {
		return err
	}
	if err := validateScript(t.Script); err != nil {
		return err
	}
	return nil
}

func validateScript(script string) error {
	if strings.TrimSpace(script) == "" {
		return errors.New("脚本不能为空")
	}
	if len(script) > maxScriptBytes {
		return fmt.Errorf("脚本不能超过 %d KiB", maxScriptBytes>>10)
	}
	if strings.ContainsRune(script, '\x00') {
		return errors.New("脚本不能包含 NUL 字符")
	}
	return nil
}

// ValidateSchedule accepts a strict, numeric, five-field cron expression.
func ValidateSchedule(schedule string) error {
	if strings.ContainsAny(schedule, "\r\n\x00") {
		return errors.New("Cron 表达式不能包含控制字符")
	}
	fields := strings.Fields(schedule)
	if len(fields) != 5 {
		return errors.New("Cron 表达式必须恰好包含 5 个字段")
	}
	limits := [][2]int{{0, 59}, {0, 23}, {1, 31}, {1, 12}, {0, 7}}
	for i, field := range fields {
		if err := validateCronField(field, limits[i][0], limits[i][1]); err != nil {
			return fmt.Errorf("Cron 第 %d 个字段无效: %w", i+1, err)
		}
	}
	return nil
}

func validateCronField(field string, min, max int) error {
	if field == "" {
		return errors.New("字段为空")
	}
	for _, item := range strings.Split(field, ",") {
		if item == "" {
			return errors.New("列表项为空")
		}
		base, stepText, hasStep := strings.Cut(item, "/")
		if hasStep {
			step, err := strconv.Atoi(stepText)
			if err != nil || step < 1 || step > max-min+1 {
				return errors.New("步长超出范围")
			}
		}
		if base == "*" {
			continue
		}
		left, right, hasRange := strings.Cut(base, "-")
		start, err := strconv.Atoi(left)
		if err != nil || start < min || start > max {
			return errors.New("数值超出范围")
		}
		if hasRange {
			end, err := strconv.Atoi(right)
			if err != nil || end < min || end > max || end < start {
				return errors.New("范围无效")
			}
		} else if hasStep {
			return errors.New("步长只能用于 * 或范围")
		}
	}
	return nil
}

// List returns a stable copy, newest updates first.
func (m *Manager) List() []Task {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := append([]Task(nil), m.tasks...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	return out
}

// Get returns a copy of a task.
func (m *Manager) Get(id string) (Task, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, task := range m.tasks {
		if task.ID == id {
			return task, true
		}
	}
	return Task{}, false
}

// Create validates and persists a task.
func (m *Manager) Create(name, schedule, script string, enabled bool) (Task, error) {
	if err := ValidateSchedule(schedule); err != nil {
		return Task{}, err
	}
	idBytes := make([]byte, 16)
	if _, err := rand.Read(idBytes); err != nil {
		return Task{}, fmt.Errorf("生成任务 ID: %w", err)
	}
	now := m.now().UTC()
	task := Task{
		ID:        hex.EncodeToString(idBytes),
		Name:      strings.TrimSpace(name),
		Schedule:  strings.Join(strings.Fields(schedule), " "),
		Script:    normalizeScript(script),
		Enabled:   enabled,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := validateTask(task); err != nil {
		return Task{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.readyLocked(); err != nil {
		return Task{}, err
	}
	if len(m.tasks) >= maxTasks {
		return Task{}, fmt.Errorf("计划任务最多允许 %d 个", maxTasks)
	}
	next := append(append([]Task(nil), m.tasks...), task)
	if err := m.apply(next); err != nil {
		return Task{}, err
	}
	m.tasks = next
	return task, nil
}

// Update replaces the editable fields of an existing task.
func (m *Manager) Update(id, name, schedule, script string, enabled bool) error {
	if err := ValidateSchedule(schedule); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.readyLocked(); err != nil {
		return err
	}
	next := append([]Task(nil), m.tasks...)
	found := false
	for i := range next {
		if next[i].ID != id {
			continue
		}
		next[i].Name = strings.TrimSpace(name)
		next[i].Schedule = strings.Join(strings.Fields(schedule), " ")
		next[i].Script = normalizeScript(script)
		next[i].Enabled = enabled
		next[i].UpdatedAt = m.now().UTC()
		if err := validateTask(next[i]); err != nil {
			return err
		}
		found = true
		break
	}
	if !found {
		return os.ErrNotExist
	}
	if err := m.apply(next); err != nil {
		return err
	}
	m.tasks = next
	return nil
}

// SetEnabled changes only the enabled state.
func (m *Manager) SetEnabled(id string, enabled bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.readyLocked(); err != nil {
		return err
	}
	next := append([]Task(nil), m.tasks...)
	found := false
	for i := range next {
		if next[i].ID == id {
			next[i].Enabled = enabled
			next[i].UpdatedAt = m.now().UTC()
			found = true
			break
		}
	}
	if !found {
		return os.ErrNotExist
	}
	if err := m.apply(next); err != nil {
		return err
	}
	m.tasks = next
	return nil
}

// Delete removes a task and its script.
func (m *Manager) Delete(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.readyLocked(); err != nil {
		return err
	}
	next := make([]Task, 0, len(m.tasks))
	found := false
	for _, task := range m.tasks {
		if task.ID == id {
			found = true
			continue
		}
		next = append(next, task)
	}
	if !found {
		return os.ErrNotExist
	}
	if err := m.apply(next); err != nil {
		return err
	}
	m.tasks = next
	if err := os.Remove(m.scriptPath(id)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("删除任务脚本: %w", err)
	}
	return nil
}

// RunNow starts a task directly without invoking a shell command string.
func (m *Manager) RunNow(id string) error {
	m.mu.Lock()
	if err := m.readyLocked(); err != nil {
		m.mu.Unlock()
		return err
	}
	var task *Task
	for i := range m.tasks {
		if m.tasks[i].ID == id {
			copyTask := m.tasks[i]
			task = &copyTask
			break
		}
	}
	if task == nil {
		m.mu.Unlock()
		return os.ErrNotExist
	}
	if err := m.writeScript(*task); err != nil {
		m.mu.Unlock()
		return err
	}
	logFile, err := os.OpenFile(m.paths.LogFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		m.mu.Unlock()
		return fmt.Errorf("打开任务日志: %w", err)
	}
	if err := logFile.Chmod(0o600); err != nil {
		logFile.Close()
		m.mu.Unlock()
		return fmt.Errorf("设置任务日志权限: %w", err)
	}
	cmd := exec.Command(m.scriptPath(id))
	cmd.Stdout, cmd.Stderr = logFile, logFile
	if err := cmd.Start(); err != nil {
		logFile.Close()
		m.mu.Unlock()
		return fmt.Errorf("启动任务: %w", err)
	}
	m.mu.Unlock()
	go func() {
		_ = cmd.Wait()
		_ = logFile.Close()
	}()
	return nil
}

// ReadLog returns at most the newest maxBytes bytes of task output.
func (m *Manager) ReadLog(maxBytes int64) (string, error) {
	if maxBytes < 1 {
		maxBytes = 32 << 10
	}
	if maxBytes > 1<<20 {
		maxBytes = 1 << 20
	}
	file, err := os.Open(m.paths.LogFile)
	if errors.Is(err, os.ErrNotExist) {
		return "（暂无执行日志）", nil
	}
	if err != nil {
		return "", fmt.Errorf("打开任务日志: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", fmt.Errorf("读取任务日志状态: %w", err)
	}
	start := info.Size() - maxBytes
	if start < 0 {
		start = 0
	}
	if _, err := file.Seek(start, 0); err != nil {
		return "", fmt.Errorf("定位任务日志: %w", err)
	}
	data := make([]byte, info.Size()-start)
	n, err := file.Read(data)
	if err != nil {
		return "", fmt.Errorf("读取任务日志: %w", err)
	}
	data = data[:n]
	if start > 0 {
		if newline := strings.IndexByte(string(data), '\n'); newline >= 0 {
			data = data[newline+1:]
		}
	}
	if len(data) == 0 {
		return "（暂无执行日志）", nil
	}
	return string(data), nil
}

func normalizeScript(script string) string {
	script = strings.ReplaceAll(script, "\r\n", "\n")
	script = strings.ReplaceAll(script, "\r", "\n")
	if strings.HasPrefix(script, "#!") {
		return script
	}
	return "#!/bin/sh\nset -eu\n" + script
}

func (m *Manager) scriptPath(id string) string {
	return filepath.Join(m.paths.ScriptDir, id+".sh")
}

func (m *Manager) writeScript(task Task) error {
	if err := os.MkdirAll(m.paths.ScriptDir, 0o700); err != nil {
		return fmt.Errorf("创建任务脚本目录: %w", err)
	}
	if err := os.Chmod(m.paths.ScriptDir, 0o700); err != nil {
		return fmt.Errorf("设置任务脚本目录权限: %w", err)
	}
	return atomicWrite(m.scriptPath(task.ID), []byte(task.Script), 0o700)
}

func (m *Manager) commit(tasks []Task) error {
	if err := m.ensureLogFile(); err != nil {
		return err
	}
	for _, task := range tasks {
		if err := validateTask(task); err != nil {
			return err
		}
		if err := m.writeScript(task); err != nil {
			return err
		}
	}
	cronData := m.renderCron(tasks)
	if err := os.MkdirAll(filepath.Dir(m.paths.CronFile), 0o755); err != nil {
		return fmt.Errorf("创建 Cron 目录: %w", err)
	}
	if err := atomicWrite(m.paths.CronFile, cronData, 0o644); err != nil {
		return fmt.Errorf("写入 Cron 文件: %w", err)
	}
	stateData, err := json.MarshalIndent(tasks, "", "  ")
	if err != nil {
		return fmt.Errorf("编码计划任务状态: %w", err)
	}
	stateData = append(stateData, '\n')
	if err := atomicWrite(m.paths.StateFile, stateData, 0o600); err != nil {
		return fmt.Errorf("写入计划任务状态: %w", err)
	}
	return nil
}

func (m *Manager) ensureLogFile() error {
	file, err := os.OpenFile(m.paths.LogFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("创建任务日志: %w", err)
	}
	defer file.Close()
	if err := file.Chmod(0o600); err != nil {
		return fmt.Errorf("设置任务日志权限: %w", err)
	}
	return nil
}

// apply attempts to restore the previously active scripts, cron file and
// state if a multi-file commit fails partway through.
func (m *Manager) apply(next []Task) error {
	previous := append([]Task(nil), m.tasks...)
	if err := m.commit(next); err != nil {
		rollbackErr := m.commit(previous)
		m.removeScriptsNotIn(next, previous)
		if rollbackErr != nil {
			return fmt.Errorf("%v；回滚也失败: %w", err, rollbackErr)
		}
		return err
	}
	return nil
}

func (m *Manager) removeScriptsNotIn(candidates, keep []Task) {
	keepIDs := make(map[string]bool, len(keep))
	for _, task := range keep {
		keepIDs[task.ID] = true
	}
	for _, task := range candidates {
		if !keepIDs[task.ID] {
			_ = os.Remove(m.scriptPath(task.ID))
		}
	}
}

func (m *Manager) renderCron(tasks []Task) []byte {
	var b strings.Builder
	b.WriteString("# Generated by NaivePanel. Do not edit.\n")
	b.WriteString("SHELL=/bin/sh\nPATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin\n\n")
	for _, task := range tasks {
		if !task.Enabled {
			continue
		}
		fmt.Fprintf(&b, "# %s (%s)\n%s root %s >> %s 2>&1\n",
			sanitizeComment(task.Name), task.ID, task.Schedule, m.scriptPath(task.ID), m.paths.LogFile)
	}
	return []byte(b.String())
}

func sanitizeComment(value string) string {
	value = strings.ReplaceAll(value, "\n", " ")
	value = strings.ReplaceAll(value, "\r", " ")
	return strings.TrimSpace(value)
}

func atomicWrite(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	return nil
}
