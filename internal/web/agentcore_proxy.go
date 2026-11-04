package web

import (
	"bytes"
	"context"
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
	case method == http.MethodGet && path == "/conversations":
		return "/v1/conversations", true
	case method == http.MethodGet && len(parts) == 3 && parts[1] == "conversations" && agentCoreID.MatchString(parts[2]):
		return "/v1/conversations/" + parts[2], true
	case method == http.MethodGet && len(parts) == 4 && parts[1] == "conversations" && agentCoreID.MatchString(parts[2]) && parts[3] == "logs":
		return "/v1/conversations/" + parts[2] + "/logs", true
	case method == http.MethodPost && (path == "/chat" || path == "/chat/stream"):
		return "/v1" + path, true
	case method == http.MethodPost && len(parts) == 4 && parts[1] == "chat" && agentCoreID.MatchString(parts[2]) && parts[3] == "stop":
		return "/v1/chat/" + parts[2] + "/stop", true
	}
	return "", false
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
	timeout := 10 * time.Minute
	if path == "/v1/chat/stream" {
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
	proxy := &httputil.ReverseProxy{
		Transport:     agentCoreTransport,
		FlushInterval: -1,
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.Out.URL.Path = path
			pr.Out.URL.RawPath = ""
			query := url.Values{}
			if path == "/v1/conversations" {
				for _, key := range []string{"source", "limit", "offset"} {
					if values, exists := pr.In.URL.Query()[key]; exists {
						query[key] = values
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
			if path == "/v1/chat/stream" {
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
			if path == "/v1/chat/stream" {
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
