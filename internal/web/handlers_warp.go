package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/kinmeic/NaivePanel/internal/warp"
)

type warpService interface {
	State() warp.State
	Install() error
	Register() error
	Connect() error
	Disconnect() error
	SetPort(int) error
	BindLicense(string) error
	VerifyEgress(context.Context) (warp.EgressResult, error)
}

type warpPageData struct {
	warp.State
	SuggestedPort int
	Operation     *serviceOperation
}

func (s *Server) handleWARP(w http.ResponseWriter, r *http.Request) {
	state := s.WARP.State()
	port := state.Port
	if port == 0 {
		port = warp.DefaultPort
	}
	data := warpPageData{State: state, SuggestedPort: port}
	s.serviceOpsMu.Lock()
	if op := s.serviceOps[s.activeServiceOps[warp.Unit]]; op != nil && !op.Done {
		snapshot := *op
		data.Operation = &snapshot
	}
	s.serviceOpsMu.Unlock()
	s.render(w, r, "warp", "WARP", data)
}

func (s *Server) handleWARPService(w http.ResponseWriter, r *http.Request) {
	var run func() error
	var label, success string
	switch r.FormValue("action") {
	case "install":
		run, label, success = s.WARP.Install, "安装 WARP", "WARP 已安装，服务已启动；请注册后连接"
	case "register":
		run, label, success = s.WARP.Register, "注册 WARP", "WARP 注册完成，proxy 模式已配置"
	case "connect":
		run, label, success = s.WARP.Connect, "连接 WARP", "WARP 连接请求已提交，正在建立连接"
	case "disconnect":
		run, label, success = s.WARP.Disconnect, "断开 WARP", "WARP 断开请求已提交"
	default:
		s.warpInputError(w, r, "未知 WARP 操作")
		return
	}
	s.startWARPOperation(w, r, label, success, run)
}

func (s *Server) handleWARPSettings(w http.ResponseWriter, r *http.Request) {
	port, err := strconv.Atoi(r.FormValue("port"))
	if err == nil {
		err = warp.ValidatePort(port)
	}
	if err != nil {
		s.warpInputError(w, r, "端口必须在 1–65535 之间")
		return
	}
	// Mode is always enforced by the manager, never accepted from a form.
	s.startWARPOperation(w, r, "保存 WARP 代理设置", "WARP 代理设置已生效", func() error { return s.WARP.SetPort(port) })
}

func (s *Server) handleWARPLicense(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimSpace(r.FormValue("license"))
	if err := warp.ValidateLicense(key); err != nil {
		s.warpInputError(w, r, err.Error())
		return
	}
	s.startWARPOperation(w, r, "绑定/变更 WARP license", "WARP license 已绑定/变更", func() error { return s.WARP.BindLicense(key) })
}

func (s *Server) warpInputError(w http.ResponseWriter, r *http.Request, message string) {
	if wantsAsyncServiceOperation(r) {
		writeWARPJSON(w, http.StatusBadRequest, map[string]string{"message": message})
		return
	}
	s.setFlash(w, message)
	s.redirect(w, r, "/warp")
}

// Package installs can exceed the panel's HTTP write timeout. All WARP actions
// share one background slot so double clicks cannot queue conflicting changes.
func (s *Server) startWARPOperation(w http.ResponseWriter, r *http.Request, label, success string, run func() error) {
	now := time.Now()
	s.serviceOpsMu.Lock()
	if op := s.serviceOps[s.activeServiceOps[warp.Unit]]; op != nil && !op.Done {
		s.serviceOpsMu.Unlock()
		if wantsAsyncServiceOperation(r) {
			writeWARPJSON(w, http.StatusConflict, map[string]string{"message": "WARP 操作正在执行，请等待完成后重试"})
		} else {
			s.setFlash(w, "WARP 操作正在执行，请等待完成后重试")
			s.redirect(w, r, "/warp")
		}
		return
	}
	s.pruneServiceOperationsLocked(now)
	id, err := newServiceOperationID()
	if err != nil {
		s.serviceOpsMu.Unlock()
		http.Error(w, "创建 WARP 操作失败", http.StatusInternalServerError)
		return
	}
	op := &serviceOperation{ID: id, Service: warp.Unit, State: "queued", Message: label + "请求已提交", StartedAt: now, UpdatedAt: now}
	s.serviceOps[id], s.activeServiceOps[warp.Unit] = op, id
	snapshot := *op
	s.serviceOpsMu.Unlock()
	go func() {
		s.updateServiceOperation(id, "running", "正在"+label+"，请稍候…", false, false)
		if err := run(); err != nil {
			s.finishServiceOperation(id, warp.Unit, false, fmt.Sprintf("%s失败：%v", label, err))
			return
		}
		s.finishServiceOperation(id, warp.Unit, true, success)
	}()
	if wantsAsyncServiceOperation(r) {
		s.respondServiceOperation(w, r, &snapshot, "/warp")
		return
	}
	s.setFlash(w, label+"请求已提交，正在后台执行；请稍候刷新查看状态")
	s.redirect(w, r, "/warp")
}

func writeWARPJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func (s *Server) handleWARPEgress(w http.ResponseWriter, r *http.Request) {
	result, err := s.WARP.VerifyEgress(r.Context())
	if err != nil {
		if wantsAsyncServiceOperation(r) {
			writeWARPJSON(w, http.StatusBadGateway, map[string]string{"message": "出口验证失败：" + err.Error()})
		} else {
			s.setFlash(w, "出口验证失败："+err.Error())
			s.redirect(w, r, "/warp")
		}
		return
	}
	if wantsAsyncServiceOperation(r) {
		writeWARPJSON(w, http.StatusOK, result)
		return
	}
	status := "已确认 WARP 出口"
	if !result.Verified {
		status = "出口验证失败：请求可达，但未经过 WARP"
	}
	s.setFlash(w, fmt.Sprintf("%s，出口 IP：%s，WARP：%s", status, result.IP, result.WARP))
	s.redirect(w, r, "/warp")
}
