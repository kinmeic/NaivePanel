package web

import (
	"encoding/json"
	"log"
	"net/http"

	"github.com/kinmeic/NaivePanel/internal/fail2ban"
)

type fail2banService interface {
	Installed() bool
	Snapshot() fail2ban.Snapshot
	Install() error
	Action(string) error
	ReadRules() (string, error)
	ApplyRules(string) error
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
			s.redirect(w, r, "/security")
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
		s.respondServiceOperation(w, r, operation, "/security")
	} else {
		s.setFlash(w, "fail2ban "+label+"请求已提交，请稍候刷新账号安全页查看状态")
		s.redirect(w, r, "/security")
	}
}

func (s *Server) handleFail2banRules(w http.ResponseWriter, r *http.Request) {
	content := r.FormValue("rules")
	if err := s.Fail2ban.ApplyRules(content); err != nil {
		data := s.securityData(r)
		data["Fail2banRules"] = content
		data["Fail2banRulesError"] = err.Error()
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusBadRequest)
		s.render(w, r, "security", "账号安全", data)
		return
	}
	s.setFlash(w, "fail2ban 规则已校验并保存；运行中的服务已重新加载，停止状态下将在下次启动时生效")
	s.redirect(w, r, "/security")
}
