package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// handleAgentCoreOptions exposes only non-secret composer configuration.
func (s *Server) handleAgentCoreOptions(w http.ResponseWriter, r *http.Request) {
	data, err := s.Agent.ReadConfig()
	if err != nil {
		http.Error(w, "无法读取 AgentCore 配置", http.StatusServiceUnavailable)
		return
	}
	var cfg struct {
		LLM struct {
			Model   string `yaml:"model"`
			Effort  string `yaml:"reasoning_effort"`
			Context int    `yaml:"context_window"`
			Output  int    `yaml:"max_output_tokens"`
		} `yaml:"llm"`
		Permission struct {
			Mode string `yaml:"mode"`
		} `yaml:"permission"`
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		http.Error(w, "AgentCore 配置格式错误", http.StatusServiceUnavailable)
		return
	}
	if cfg.LLM.Context <= 0 {
		cfg.LLM.Context = 200000
	}
	if cfg.LLM.Output <= 0 {
		cfg.LLM.Output = 32000
	}
	if cfg.Permission.Mode == "" {
		cfg.Permission.Mode = "build"
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(map[string]any{
		"model": cfg.LLM.Model, "reasoning_effort": cfg.LLM.Effort,
		"context_window": cfg.LLM.Context, "max_output_tokens": cfg.LLM.Output,
		"approval_mode": cfg.Permission.Mode,
	})
}

var agentCoreID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

// No environment proxy, DNS lookup, redirect following, or browser credentials.
var agentCoreTransport = &http.Transport{
	Proxy:                 nil,
	DialContext:           (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
	ResponseHeaderTimeout: 10 * time.Minute,
	IdleConnTimeout:       90 * time.Second,
	MaxIdleConnsPerHost:   4,
	DisableCompression:    true,
}

func agentCoreTarget(data []byte) (*url.URL, string, error) {
	var cfg struct {
		Server struct {
			Host  string `yaml:"host"`
			Port  int    `yaml:"port"`
			Token string `yaml:"auth_token"`
		} `yaml:"server"`
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, "", err
	}
	host := cfg.Server.Host
	if host == "localhost" {
		host = "127.0.0.1"
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() || cfg.Server.Port < 1 || cfg.Server.Port > 65535 {
		return nil, "", errors.New("AgentCore requires an explicit loopback host and valid port")
	}
	token := cfg.Server.Token
	if strings.ContainsAny(token, "\r\n\x00") || strings.Contains(token, "$") {
		return nil, "", errors.New("AgentCore token must be resolved in the configuration")
	}
	return &url.URL{Scheme: "http", Host: net.JoinHostPort(ip.String(), strconv.Itoa(cfg.Server.Port))}, token, nil
}

func agentCoreRoute(method, path string) (string, bool) {
	parts := strings.Split(path, "/")
	switch {
	case method == http.MethodGet && len(parts) == 5 && parts[1] == "runs" && agentCoreID.MatchString(parts[2]) && parts[3] == "events" && parts[4] == "stream":
		return "/v1/runs/" + parts[2] + "/events/stream", true
	case method == http.MethodGet && path == "/approvals":
		return "/v1/approvals", true
	case method == http.MethodPost && len(parts) == 3 && parts[1] == "approvals" && agentCoreID.MatchString(parts[2]):
		return "/v1/approvals/" + parts[2], true
	case method == http.MethodGet && path == "/conversations":
		return "/v1/conversations", true
	case method == http.MethodGet && len(parts) == 3 && parts[1] == "conversations" && agentCoreID.MatchString(parts[2]):
		return "/v1/conversations/" + parts[2], true
	case method == http.MethodGet && len(parts) == 4 && parts[1] == "conversations" && agentCoreID.MatchString(parts[2]) && (parts[3] == "logs" || parts[3] == "runs"):
		return "/v1/conversations/" + parts[2] + "/" + parts[3], true
	case method == http.MethodPost && len(parts) == 4 && parts[1] == "conversations" && agentCoreID.MatchString(parts[2]) && parts[3] == "delete":
		return "/v1/conversations/" + parts[2], true
	case method == http.MethodPost && (path == "/chat" || path == "/chat/stream"):
		return "/v1" + path, true
	case method == http.MethodPost && len(parts) == 4 && parts[1] == "chat" && agentCoreID.MatchString(parts[2]) && (parts[3] == "stop" || parts[3] == "clarify"):
		return "/v1/chat/" + parts[2] + "/" + parts[3], true
	}
	return "", false
}

// Preference changes need a conservative active-run preflight. Permanent
// deletion uses the upstream endpoint's atomic chat/workflow guard instead.
func agentCoreDeletionReady(w http.ResponseWriter, r *http.Request, target *url.URL, token, path string) bool {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	u := *target
	u.Path = path + "/runs"
	u.RawQuery = "limit=500"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		http.Error(w, "Cannot verify AgentCore run state", http.StatusBadGateway)
		return false
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := agentCoreTransport.RoundTrip(req)
	if err != nil {
		http.Error(w, "Cannot verify AgentCore run state", http.StatusBadGateway)
		return false
	}
	defer resp.Body.Close()
	var runs []*struct {
		Status string `json:"status"`
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, (8<<20)+1))
	if resp.StatusCode != http.StatusOK || err != nil || len(body) > 8<<20 || json.Unmarshal(body, &runs) != nil {
		http.Error(w, "Cannot verify AgentCore run state", http.StatusBadGateway)
		return false
	}
	for _, run := range runs {
		if run == nil {
			http.Error(w, "Cannot verify AgentCore run state", http.StatusBadGateway)
			return false
		}
		switch run.Status {
		case "completed", "failed", "cancelled":
		case "pending", "running", "waiting_clarification", "waiting_approval":
			http.Error(w, "Conversation has active runs", http.StatusConflict)
			return false
		default:
			http.Error(w, "Cannot verify AgentCore run state", http.StatusBadGateway)
			return false
		}
	}
	// ConversationRuns has a hard limit of 500 and no pagination.
	if len(runs) >= 500 {
		http.Error(w, "Cannot safely modify conversation: run history limit reached", http.StatusConflict)
		return false
	}
	return true
}

// handleAgentCoreAPI must be registered behind the panel's protect middleware
// (session authentication and CSRF). It intentionally does not register routes.
func (s *Server) handleAgentCoreAPI(w http.ResponseWriter, r *http.Request) {
	prefix := "/agentcore/api"
	if s.Cfg != nil {
		prefix = s.Cfg.BasePath + prefix
	}
	if !strings.HasPrefix(r.URL.Path, prefix+"/") || r.URL.RawPath != "" {
		http.NotFound(w, r)
		return
	}
	path, ok := agentCoreRoute(r.Method, strings.TrimPrefix(r.URL.Path, prefix))
	if !ok {
		http.NotFound(w, r)
		return
	}
	if s.Agent == nil {
		http.Error(w, "AgentCore configuration unavailable", http.StatusServiceUnavailable)
		return
	}
	data, err := s.Agent.ReadConfig()
	if err != nil {
		http.Error(w, "AgentCore configuration unavailable", http.StatusServiceUnavailable)
		return
	}
	target, token, err := agentCoreTarget(data)
	if err != nil {
		http.Error(w, "AgentCore requires valid loopback configuration and a resolved token", http.StatusServiceUnavailable)
		return
	}
	// Bound both synchronous calls and streams, while preserving browser cancellation.
	streaming := strings.HasSuffix(path, "/stream")
	timeout := 10 * time.Minute
	if streaming {
		timeout = time.Hour
	}
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()
	r = r.WithContext(ctx)
	// Buffer a bounded request before contacting upstream: oversized bodies never
	// start a run, and a slow browser cannot hold an upstream upload indefinitely.
	if r.Method == http.MethodPost {
		body, readErr := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
		if readErr != nil {
			status := http.StatusBadRequest
			var tooLarge *http.MaxBytesError
			if errors.As(readErr, &tooLarge) {
				status = http.StatusRequestEntityTooLarge
			}
			http.Error(w, "Invalid AgentCore request body", status)
			return
		}
		r.Body.Close()
		r.Body = io.NopCloser(bytes.NewReader(body))
		r.ContentLength = int64(len(body))
	}
	deleting := r.Method == http.MethodPost && strings.HasPrefix(path, "/v1/conversations/")

	proxy := &httputil.ReverseProxy{
		Transport:     agentCoreTransport,
		FlushInterval: -1,
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.Out.URL.Path = path
			pr.Out.URL.RawPath = ""
			// Rewrite only the upstream copy, after protect validated the browser POST.
			if deleting {
				pr.Out.Method = http.MethodDelete
				pr.Out.Body = nil
				pr.Out.ContentLength = 0
				pr.Out.TransferEncoding = nil
			}
			query := url.Values{}
			if deleting {
				query.Set("permanent", "true")
			}
			if path == "/v1/conversations" {
				for _, key := range []string{"source", "limit", "offset"} {
					if values, exists := pr.In.URL.Query()[key]; exists {
						query[key] = values
					}
				}
			}
			if pr.In.Method == http.MethodGet && strings.HasPrefix(path, "/v1/conversations/") && strings.HasSuffix(path, "/runs") {
				if limit, err := strconv.Atoi(pr.In.URL.Query().Get("limit")); err == nil && limit > 0 && limit <= 500 {
					query.Set("limit", strconv.Itoa(limit))
				}
			}
			if streaming && pr.In.Method == http.MethodGet {
				if after := pr.In.URL.Query().Get("after"); after != "" {
					if cursor, err := strconv.ParseUint(after, 10, 64); err == nil {
						query.Set("after", strconv.FormatUint(cursor, 10))
					}
				}
			}
			pr.Out.URL.RawQuery = query.Encode()
			pr.Out.Header = make(http.Header)
			if pr.Out.Method == http.MethodPost {
				pr.Out.Header.Set("Content-Type", "application/json")
			}
			if token != "" {
				pr.Out.Header.Set("Authorization", "Bearer "+token)
			}
			if streaming {
				pr.Out.Header.Set("Accept", "text/event-stream")
			}
		},
		ModifyResponse: func(resp *http.Response) error {
			// Do not relay cookies, redirects, CORS, or hop-by-hop nominated headers.
			headers := make(http.Header)
			for _, key := range []string{"Content-Type", "X-Run-ID", "X-Request-ID", "Retry-After"} {
				if value := resp.Header.Get(key); value != "" {
					headers.Set(key, value)
				}
			}
			headers.Set("Cache-Control", "no-store")
			headers.Set("X-Content-Type-Options", "nosniff")
			if streaming {
				headers.Set("X-Accel-Buffering", "no")
			}
			resp.Header = headers
			resp.Trailer = nil
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			status := http.StatusBadGateway
			if errors.Is(r.Context().Err(), context.DeadlineExceeded) {
				status = http.StatusGatewayTimeout
			}
			http.Error(w, "AgentCore upstream request failed", status)
		},
	}
	proxy.ServeHTTP(w, r)
}
