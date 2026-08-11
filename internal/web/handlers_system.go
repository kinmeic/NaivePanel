package web

import (
	"encoding/json"
	"net/http"
)

func (s *Server) handleSystemStats(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := json.NewEncoder(w).Encode(s.Stats.Sample()); err != nil {
		http.Error(w, "编码系统状态失败", http.StatusInternalServerError)
	}
}
