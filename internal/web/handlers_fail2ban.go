package web

import (
	"encoding/json"
	"log"
	"net/http"
	"slices"
	"strconv"

	"github.com/kinmeic/NaivePanel/internal/fail2ban"
)

type fail2banService interface {
	Installed() bool
	Snapshot() fail2ban.Snapshot
	Install() error
	Action(string) error
	ReadRuleFile() (fail2ban.RuleFile, error)
	ApplyRules(string) error
}

const fail2banPageSize = 20

func (s *Server) fail2banPending() string {
	s.serviceOpsMu.Lock()
	defer s.serviceOpsMu.Unlock()
	if op := s.serviceOps[s.activeServiceOps["fail2ban"]]; op != nil {
		return op.Message
	}
	return ""
}

func fail2banSSHDState(state fail2ban.Snapshot) string {
	switch {
	case !state.Installed:
		return "未安装"
	case !state.Active:
		return "服务未运行"
	case state.StatusError != "":
		return "无法读取"
	case slices.Contains(state.Jails, "sshd"):
		return "已启用"
	default:
		return "未启用"
	}
}

func (s *Server) handleFail2ban(w http.ResponseWriter, r *http.Request) {
	state := s.Fail2ban.Snapshot()
	total := len(state.Records)
	pages := max(1, (total+fail2banPageSize-1)/fail2banPageSize)
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	page = min(max(1, page), pages)
	start, end := (page-1)*fail2banPageSize, min(page*fail2banPageSize, total)
	state.Records = state.Records[start:end]
	first := 0
	if total > 0 {
		first = start + 1
	}
	s.render(w, r, "fail2ban", "Fail2ban", map[string]any{
		"Fail2ban": state, "Pending": s.fail2banPending(), "SSHDState": fail2banSSHDState(state),
		"Page": page, "Pages": pages, "Total": total, "First": first, "Last": end,
		"PreviousPage": max(1, page-1), "NextPage": min(pages, page+1),
	})
}

func (s *Server) fail2banConfigData() map[string]any {
	file, err := s.Fail2ban.ReadRuleFile()
	state := s.Fail2ban.Snapshot()
	data := map[string]any{
		"RuleFile": file, "Rules": file.Content, "Readable": err == nil,
		"Fail2ban": state, "SSHDState": fail2banSSHDState(state), "Pending": s.fail2banPending(), "ActiveTab": "base",
	}
	if err != nil {
		data["Error"] = "读取规则失败: " + err.Error()
	}
	return data
}

func (s *Server) handleFail2banConfig(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, "fail2ban_config", "Fail2ban 配置", s.fail2banConfigData())
}

func (s *Server) handleFail2banInstall(w http.ResponseWriter, r *http.Request) {
	s.startFail2banWork(w, r, "安装", s.Fail2ban.Install)
}

func (s *Server) handleFail2banService(w http.ResponseWriter, r *http.Request) {
	verb := r.FormValue("action")
	labels := map[string]string{"start": "启动", "stop": "停止", "restart": "重启", "enable": "开启自启", "disable": "关闭自启"}
	label, ok := labels[verb]
	if !ok || !s.Fail2ban.Installed() {
		message := "未知 fail2ban 操作"
		if !s.Fail2ban.Installed() {
			message = "fail2ban 尚未安装"
		}
		if wantsAsyncServiceOperation(r) {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"message": message})
		} else {
			s.setFlash(w, message)
			s.redirect(w, r, "/fail2ban")
		}
		return
	}
	s.startFail2banWork(w, r, label, func() error { return s.Fail2ban.Action(verb) })
}

func (s *Server) startFail2banWork(w http.ResponseWriter, r *http.Request, label string, work func() error) {
	operation, _, err := s.beginServiceWork("fail2ban", "fail2ban "+label+"请求已提交", func(id string) {
		s.updateServiceOperation(id, "running", "正在"+label+" fail2ban，请稍候…", false, false)
		if err := work(); err != nil {
			log.Printf("fail2ban %s失败: %v", label, err)
			message := "fail2ban " + label + "失败: " + err.Error()
			if len([]rune(message)) > 500 {
				message = string([]rune(message)[:500]) + "…（详情见面板服务日志）"
			}
			s.finishServiceOperation(id, "fail2ban", false, message)
			return
		}
		s.finishServiceOperation(id, "fail2ban", true, "fail2ban "+label+"完成")
	})
	if err != nil {
		http.Error(w, "提交 fail2ban 操作失败", http.StatusInternalServerError)
		return
	}
	if wantsAsyncServiceOperation(r) {
		s.respondServiceOperation(w, r, operation, "/fail2ban")
	} else {
		s.setFlash(w, "fail2ban "+label+"请求已提交，请稍候刷新 Fail2ban 页查看状态")
		s.redirect(w, r, "/fail2ban")
	}
}

func (s *Server) handleFail2banRules(w http.ResponseWriter, r *http.Request) {
	content := r.FormValue("rules")
	if err := s.Fail2ban.ApplyRules(content); err != nil {
		data := s.fail2banConfigData()
		data["Rules"] = content
		data["Error"] = err.Error()
		if tab := r.FormValue("editor_tab"); tab == "jails" || tab == "raw" {
			data["ActiveTab"] = tab
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusBadRequest)
		s.render(w, r, "fail2ban_config", "Fail2ban 配置", data)
		return
	}
	s.setFlash(w, "fail2ban 规则已校验并保存；运行中的服务已重新加载，停止状态下将在下次启动时生效")
	s.redirect(w, r, "/fail2ban")
}
