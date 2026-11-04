package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/kinmeic/NaivePanel/internal/agentcore"
	"github.com/kinmeic/NaivePanel/internal/selfupdate"
	"github.com/kinmeic/NaivePanel/internal/sysd"
	"gopkg.in/yaml.v3"
)

func (s *Server) handleAgentCore(w http.ResponseWriter, r *http.Request) {
	installed := s.Agent.Installed()
	lines, logs, logErr := readServiceLog(r, "agent-core")
	s.render(w, r, "agentcore", "AgentCore", map[string]any{"Installed": installed, "Active": installed && sysd.IsActive("agent-core"), "Enabled": installed && sysd.IsEnabled("agent-core"), "Version": s.Agent.VersionTag(), "ConfigPath": s.Cfg.AgentCore.ConfigPath, "WorkDir": s.Cfg.AgentCore.WorkDir, "LogLines": lines, "LogContent": logs, "LogError": logErr})
}
func (s *Server) handleAgentCoreInstall(w http.ResponseWriter, r *http.Request) {
	s.agentMu.Lock()
	defer s.agentMu.Unlock()
	tag, err := s.Agent.Install(s.Cfg.GeoSnapshot().Mirror)
	if err != nil {
		s.setFlash(w, "AgentCore 安装/更新失败: "+err.Error())
		s.redirect(w, r, "/settings")
		return
	}
	s.setFlash(w, "AgentCore "+tag+" 安装完成并已启动")
	s.redirect(w, r, "/settings")
}
func (s *Server) handleAgentCoreUpdateCheck(w http.ResponseWriter, r *http.Request) {
	if !s.Agent.Installed() {
		s.setFlash(w, "AgentCore 尚未安装")
		s.redirect(w, r, "/settings")
		return
	}
	rel, err := agentcore.LatestRelease(s.Cfg.GeoSnapshot().Mirror)
	if err != nil {
		s.setFlash(w, "检查 AgentCore 更新失败: "+err.Error())
	} else {
		current := s.Agent.VersionTag()
		switch {
		case current == "":
			s.setFlash(w, "最新 AgentCore 版本为 "+rel.TagName+"；当前版本号无法识别")
		case selfupdate.Newer(current, rel.TagName):
			s.setFlash(w, fmt.Sprintf("发现 AgentCore 新版本 %s（当前 %s）", rel.TagName, current))
		default:
			s.setFlash(w, fmt.Sprintf("AgentCore 已是最新版本（当前 %s）", current))
		}
	}
	s.redirect(w, r, "/settings")
}
func (s *Server) handleAgentCoreService(w http.ResponseWriter, r *http.Request) {
	action := r.FormValue("action")
	switch action {
	case "start", "stop", "restart", "enable", "disable":
		if !s.Agent.Installed() {
			s.setFlash(w, "请先安装 AgentCore")
			break
		}
		if action == "restart" {
			operation, _, err := s.beginServiceRestart("agent-core", "AgentCore", 0)
			if err != nil {
				s.setFlash(w, "重启 AgentCore 失败: "+err.Error())
				break
			}
			s.respondServiceOperation(w, r, operation, "/agentcore")
			return
		}
		if err := sysd.Action(action, "agent-core"); err != nil {
			s.setFlash(w, err.Error())
		} else {
			s.setFlash(w, "AgentCore 服务操作已完成")
		}
	default:
		s.setFlash(w, "未知操作")
	}
	s.redirect(w, r, "/agentcore")
}
func (s *Server) agentConfigData(content []byte, readErr error) map[string]any {
	data := map[string]any{"Config": string(content), "Installed": s.Agent.Installed()}
	if readErr != nil {
		data["Error"] = "读取配置失败: " + readErr.Error()
		return data
	}
	var root map[string]any
	if err := yaml.Unmarshal(content, &root); err != nil {
		data["Error"] = "配置解析失败: " + err.Error()
		return data
	}
	jsonData, err := json.Marshal(root)
	if err != nil {
		data["Error"] = "配置转换失败: " + err.Error()
		return data
	}
	data["ConfigJSON"] = string(jsonData)
	overrides := map[string]bool{}
	if entries, ok := root["skill"].(map[string]any); ok {
		for path, v := range entries {
			if props, ok := v.(map[string]any); ok {
				if enabled, ok := props["enable"].(bool); ok {
					overrides[path] = enabled
				}
			}
		}
	}
	skills, err := s.Agent.ListSkills(overrides)
	if err != nil {
		data["Error"] = "读取 Skill 失败: " + err.Error()
	}
	data["Skills"] = skills
	return data
}
func (s *Server) handleAgentCoreConfigGET(w http.ResponseWriter, r *http.Request) {
	content, err := s.Agent.ReadConfig()
	s.render(w, r, "agentcore_config", "AgentCore 配置", s.agentConfigData(content, err))
}
func (s *Server) handleAgentCoreConfigPOST(w http.ResponseWriter, r *http.Request) {
	s.agentMu.Lock()
	defer s.agentMu.Unlock()
	mode := r.FormValue("mode")
	var content []byte
	if mode == "structured" {
		var root map[string]any
		if err := json.Unmarshal([]byte(r.FormValue("structured")), &root); err != nil {
			s.render(w, r, "agentcore_config", "AgentCore 配置", map[string]any{"Error": "结构化配置格式错误: " + err.Error(), "Config": r.FormValue("config")})
			return
		}
		var err error
		content, err = yaml.Marshal(root)
		if err != nil {
			s.setFlash(w, "配置转换失败: "+err.Error())
			s.redirect(w, r, "/agentcore/config")
			return
		}
	} else {
		content = []byte(r.FormValue("config"))
	}
	active := sysd.IsActive("agent-core")
	if err := s.Agent.SaveConfig(content, active); err != nil {
		data := s.agentConfigData(content, nil)
		data["Error"] = "保存失败: " + err.Error()
		s.render(w, r, "agentcore_config", "AgentCore 配置", data)
		return
	}
	if active {
		s.setFlash(w, "AgentCore 配置已校验并保存，服务已重启")
	} else {
		s.setFlash(w, "AgentCore 配置已校验并保存，将在下次启动时生效")
	}
	s.redirect(w, r, "/agentcore")
}
func (s *Server) handleAgentCoreSkill(w http.ResponseWriter, r *http.Request) {
	s.agentMu.Lock()
	defer s.agentMu.Unlock()
	if !s.Agent.Installed() {
		s.setFlash(w, "请先安装 AgentCore")
		s.redirect(w, r, "/agentcore/config")
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	var err error
	switch r.FormValue("action") {
	case "save":
		err = s.Agent.EnsureSkillRoot(sysd.IsActive("agent-core"))
		if err == nil {
			err = s.Agent.SaveSkill(name, r.FormValue("content"))
		}
	case "delete":
		err = s.Agent.DeleteSkill(name)
	case "toggle":
		err = s.Agent.SetSkillEnabled(name, r.FormValue("enabled") == "true", sysd.IsActive("agent-core"))
	default:
		err = fmt.Errorf("未知 Skill 操作")
	}
	if err != nil {
		s.setFlash(w, "Skill 操作失败: "+err.Error())
	} else {
		s.setFlash(w, "Skill 操作已完成；请重启 AgentCore 使变更生效")
	}
	s.redirect(w, r, "/agentcore/config")
}
