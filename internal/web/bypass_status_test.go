package web

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestNewBypassStatusViewBuildsFriendlySummary(t *testing.T) {
	now := time.Date(2026, 7, 30, 23, 0, 0, 0, time.UTC)
	raw := json.RawMessage(`{
		"running": true,
		"ready": true,
		"startedAt": "2026-07-29T20:30:00Z",
		"configHash": "1234567890abcdef",
		"configRevision": 4,
		"geodataLoaded": true,
		"inbounds": [
			{"tag":"caddy-in","type":"socks","listen":"127.0.0.1:1080","state":"running","running":true}
		],
		"outbounds": [
			{"tag":"proxy","lastSuccess":"2026-07-30T22:58:00Z"},
			{"tag":"broken","lastFailure":"2026-07-30T22:59:00Z","lastError":"connection refused"}
		],
		"dnsUpstreams": [{"address":"1.1.1.1"}],
		"lastReload": {"success":true,"at":"2026-07-30T22:55:00Z"}
	}`)

	view, err := newBypassStatusView(raw, now)
	if err != nil {
		t.Fatal(err)
	}
	if view.HealthLabel != "运行正常" || view.Uptime != "1 天 2 小时" ||
		view.ConfigRevision != "第 4 版" || view.ConfigHash != "1234567890ab" ||
		view.GeodataLabel != "已加载" || view.DNSUpstreamCount != 1 {
		t.Fatalf("unexpected summary: %#v", view)
	}
	if len(view.Inbounds) != 1 || view.Inbounds[0].Description != "SOCKS 代理" ||
		view.Inbounds[0].State != "正常监听" {
		t.Fatalf("unexpected inbounds: %#v", view.Inbounds)
	}
	if len(view.Outbounds) != 2 || view.Outbounds[0].State != "最近连接成功" ||
		view.Outbounds[1].State != "最近连接失败" {
		t.Fatalf("unexpected outbounds: %#v", view.Outbounds)
	}
	if !strings.Contains(view.LastReload, "已成功生效") {
		t.Fatalf("unexpected reload summary: %q", view.LastReload)
	}
}

func TestNewBypassStatusViewTreatsZeroTimesAsNoRecord(t *testing.T) {
	raw := json.RawMessage(`{
		"running":true,
		"outbounds":[{"tag":"direct","lastSuccess":"0001-01-01T00:00:00Z"}],
		"lastReload":{"success":false,"at":"0001-01-01T00:00:00Z"}
	}`)
	view, err := newBypassStatusView(raw, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Outbounds) != 1 || view.Outbounds[0].State != "等待首次连接" {
		t.Fatalf("zero time became a connection record: %#v", view.Outbounds)
	}
	if view.LastReload != "本次启动后尚未热重载" {
		t.Fatalf("zero time became a reload record: %q", view.LastReload)
	}
}

func TestBypassStatusViewRendersSummaryBeforeTechnicalDetails(t *testing.T) {
	server, err := New(testConfig(t), "test")
	if err != nil {
		t.Fatal(err)
	}
	raw := json.RawMessage(`{"running":true,"ready":true,"configRevision":2,"geodataLoaded":true}`)
	view, err := newBypassStatusView(raw, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest("GET", "/manage-test/bypasscore", nil)
	server.render(recorder, request, "bypass", "BypassCore", map[string]any{
		"Installed":  true,
		"Active":     true,
		"Enabled":    true,
		"SocksPort":  1080,
		"StatusView": view,
		"StatusRaw":  decoded,
	})
	html := recorder.Body.String()
	summaryAt := strings.Index(html, "运行正常")
	detailsAt := strings.Index(html, "查看原始状态（排障用）")
	rawAt := strings.Index(html, `&#34;configRevision&#34;`)
	if summaryAt < 0 || detailsAt < summaryAt || rawAt < detailsAt {
		t.Fatalf("status summary/details are missing or out of order:\n%s", html)
	}
}
