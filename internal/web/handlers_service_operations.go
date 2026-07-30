package web

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

const serviceOperationRetention = 10 * time.Minute

// serviceOperation is intentionally small and contains no systemctl output:
// the browser only needs safe, user-facing progress while detailed failures
// continue to be available through the panel's operation and service logs.
type serviceOperation struct {
	ID        string    `json:"id"`
	Service   string    `json:"service"`
	State     string    `json:"state"`
	Message   string    `json:"message"`
	Done      bool      `json:"done"`
	Success   bool      `json:"success"`
	StartedAt time.Time `json:"started_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type serviceOperationResponse struct {
	serviceOperation
	StatusURL string `json:"status_url"`
}

func newServiceOperationID() (string, error) {
	var raw [18]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("生成操作编号: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw[:]), nil
}

// beginServiceRestart starts at most one restart per unit. A duplicate request
// made while the first is pending/running receives the original operation ID.
func (s *Server) beginServiceRestart(unit, label string, delay time.Duration) (*serviceOperation, bool, error) {
	now := time.Now()
	s.serviceOpsMu.Lock()
	for id, operation := range s.serviceOps {
		if operation.Done && now.Sub(operation.UpdatedAt) > serviceOperationRetention {
			delete(s.serviceOps, id)
		}
	}
	if id := s.activeServiceOps[unit]; id != "" {
		if operation := s.serviceOps[id]; operation != nil && !operation.Done {
			snapshot := *operation
			s.serviceOpsMu.Unlock()
			return &snapshot, false, nil
		}
		delete(s.activeServiceOps, unit)
	}
	id, err := newServiceOperationID()
	if err != nil {
		s.serviceOpsMu.Unlock()
		return nil, false, err
	}
	operation := &serviceOperation{
		ID:        id,
		Service:   unit,
		State:     "queued",
		Message:   "重启请求已提交，正在等待安全执行",
		StartedAt: now,
		UpdatedAt: now,
	}
	s.serviceOps[id] = operation
	s.activeServiceOps[unit] = id
	snapshot := *operation
	s.serviceOpsMu.Unlock()

	go s.runServiceRestart(id, unit, label, delay)
	return &snapshot, true, nil
}

func (s *Server) runServiceRestart(id, unit, label string, delay time.Duration) {
	if delay > 0 {
		time.Sleep(delay)
	}
	s.updateServiceOperation(id, "restarting", "正在停止并重新启动 "+label, false, false)

	var err error
	if unit == "caddy" {
		s.caddyMu.Lock()
		err = s.serviceAction("restart", unit)
		s.caddyMu.Unlock()
	} else {
		err = s.serviceAction("restart", unit)
	}
	if err != nil {
		s.finishServiceOperation(id, unit, false, label+" 重启失败："+err.Error())
		return
	}

	s.updateServiceOperation(id, "checking", "重启命令已完成，正在确认服务恢复", false, false)
	deadline := time.Now().Add(12 * time.Second)
	for {
		if s.serviceActive(unit) {
			s.finishServiceOperation(id, unit, true, label+" 已恢复运行")
			return
		}
		if time.Now().After(deadline) {
			s.finishServiceOperation(id, unit, false, label+" 未在预期时间内恢复，请查看服务日志")
			return
		}
		time.Sleep(300 * time.Millisecond)
	}
}

func (s *Server) updateServiceOperation(id, state, message string, done, success bool) {
	s.serviceOpsMu.Lock()
	defer s.serviceOpsMu.Unlock()
	if operation := s.serviceOps[id]; operation != nil {
		operation.State = state
		operation.Message = message
		operation.Done = done
		operation.Success = success
		operation.UpdatedAt = time.Now()
	}
}

func (s *Server) finishServiceOperation(id, unit string, success bool, message string) {
	state := "failed"
	if success {
		state = "succeeded"
	}
	s.updateServiceOperation(id, state, message, true, success)
	s.serviceOpsMu.Lock()
	if s.activeServiceOps[unit] == id {
		delete(s.activeServiceOps, unit)
	}
	s.serviceOpsMu.Unlock()
}

func (s *Server) serviceOperationSnapshot(id string) (serviceOperation, bool) {
	s.serviceOpsMu.Lock()
	defer s.serviceOpsMu.Unlock()
	operation := s.serviceOps[id]
	if operation == nil {
		return serviceOperation{}, false
	}
	return *operation, true
}

func (s *Server) handleServiceOperation(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	// Reject malformed identifiers before touching the map. This keeps the
	// endpoint's behavior predictable even under automated probing.
	if len(id) != 24 || strings.ContainsAny(id, "/\\") {
		http.NotFound(w, r)
		return
	}
	operation, ok := s.serviceOperationSnapshot(id)
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(serviceOperationResponse{
		serviceOperation: operation,
		StatusURL:        s.Cfg.BasePath + "/service-operations/" + operation.ID,
	})
}

func wantsAsyncServiceOperation(r *http.Request) bool {
	return r.Header.Get("X-NaivePanel-Async") == "1" ||
		strings.Contains(r.Header.Get("Accept"), "application/json")
}

func (s *Server) respondServiceOperation(w http.ResponseWriter, r *http.Request, operation *serviceOperation, redirectPath string) {
	if wantsAsyncServiceOperation(r) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(serviceOperationResponse{
			serviceOperation: *operation,
			StatusURL:        s.Cfg.BasePath + "/service-operations/" + operation.ID,
		})
		return
	}
	s.setFlash(w, "重启请求已提交，系统将在后台执行；请稍候刷新查看状态")
	s.redirect(w, r, redirectPath)
}
