package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"time"
)

type agentCorePreferenceConversation struct {
	ID         string            `json:"id"`
	Metadata   map[string]string `json:"metadata"`
	ArchivedAt *string           `json:"archived_at,omitempty"`
}

// Requests are rebuilt rather than cloned so browser credentials and queries never
// reach AgentCore. RoundTrip deliberately does not follow redirects.
func agentCorePreferenceRequest(ctx context.Context, target *url.URL, token, method, path string, body []byte, result any) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	u := *target
	u.Path = path
	req, err := http.NewRequestWithContext(ctx, method, u.String(), bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := agentCoreTransport.RoundTrip(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil {
		return resp.StatusCode, err
	}
	if len(data) > 1<<20 {
		return resp.StatusCode, errors.New("upstream body too large")
	}
	if resp.StatusCode != http.StatusOK {
		return resp.StatusCode, errors.New("upstream request failed")
	}
	return resp.StatusCode, json.Unmarshal(data, result)
}

func (s *Server) handleAgentCorePreferences(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	id := r.PathValue("id")
	if r.Method != http.MethodPost || !agentCoreID.MatchString(id) || r.URL.RawPath != "" {
		http.NotFound(w, r)
		return
	}
	var input struct {
		Mode string `json:"mode"`
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4096))
	if err != nil {
		status := http.StatusBadRequest
		var large *http.MaxBytesError
		if errors.As(err, &large) {
			status = http.StatusRequestEntityTooLarge
		}
		http.Error(w, "Invalid preference request", status)
		return
	}
	if json.Unmarshal(body, &input) != nil {
		http.Error(w, "Invalid preference request", http.StatusBadRequest)
		return
	}
	switch input.Mode {
	case "plan", "build", "edit", "guarded", "yolo":
	default:
		http.Error(w, "Invalid conversation mode", http.StatusBadRequest)
		return
	}
	s.agentMu.Lock()
	defer s.agentMu.Unlock()
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
	if s.Agent == nil {
		http.Error(w, "AgentCore configuration unavailable", http.StatusServiceUnavailable)
		return
	}
	config, err := s.Agent.ReadConfig()
	if err != nil {
		http.Error(w, "AgentCore configuration unavailable", http.StatusServiceUnavailable)
		return
	}
	target, token, err := agentCoreTarget(config)
	if err != nil {
		http.Error(w, "AgentCore requires valid loopback configuration and a resolved token", http.StatusServiceUnavailable)
		return
	}
	fail := func() {
		status := http.StatusBadGateway
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			status = http.StatusGatewayTimeout
		}
		http.Error(w, "AgentCore preference update failed", status)
	}
	path := "/v1/conversations/" + id
	var before agentCorePreferenceConversation
	status, err := agentCorePreferenceRequest(ctx, target, token, http.MethodGet, path, nil, &before)
	if err != nil {
		if status == http.StatusNotFound {
			http.Error(w, "Conversation not found", http.StatusNotFound)
		} else {
			fail()
		}
		return
	}
	if before.ID != id {
		fail()
		return
	}
	if before.ArchivedAt != nil {
		http.Error(w, "Conversation is archived", http.StatusConflict)
		return
	}
	if !agentCoreDeletionReady(w, r, target, token, path) {
		return
	}
	payload, _ := json.Marshal(map[string]string{"conversation_id": id, "message": "/mode " + input.Mode, "source": "web"})
	var command struct {
		Command string                          `json:"command"`
		Action  string                          `json:"action"`
		Data    agentCorePreferenceConversation `json:"data"`
	}
	_, err = agentCorePreferenceRequest(ctx, target, token, http.MethodPost, "/v1/chat", payload, &command)
	if err != nil || command.Command != "mode" || command.Action != "preference" || command.Data.ID != id || command.Data.Metadata["mode"] != input.Mode {
		fail()
		return
	}
	var after agentCorePreferenceConversation
	_, err = agentCorePreferenceRequest(ctx, target, token, http.MethodGet, path, nil, &after)
	if err != nil || after.ID != id || after.ArchivedAt != nil || after.Metadata["mode"] != input.Mode {
		fail()
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(struct {
		ID       string            `json:"id"`
		Metadata map[string]string `json:"metadata"`
	}{after.ID, after.Metadata})
}
