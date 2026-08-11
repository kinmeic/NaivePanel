package web

import (
	"errors"
	"net/http"
	"os"
	"os/exec"

	"github.com/kinmeic/NaivePanel/internal/cronmgr"
	"github.com/kinmeic/NaivePanel/internal/sysd"
)

type cronPageData struct {
	Tasks      []cronmgr.Task
	Installed  bool
	Active     bool
	Enabled    bool
	CronFile   string
	LogFile    string
	Log        string
	LogError   string
	StateError string
}

type cronFormData struct {
	Task  cronmgr.Task
	IsNew bool
	Error string
}

func (s *Server) handleCron(w http.ResponseWriter, r *http.Request) {
	paths := s.Cron.Paths()
	logText, logErr := s.Cron.ReadLog(48 << 10)
	data := cronPageData{
		Tasks:     s.Cron.List(),
		Installed: commandExists("cron"),
		Active:    sysd.IsActive("cron"),
		Enabled:   sysd.IsEnabled("cron"),
		CronFile:  paths.CronFile,
		LogFile:   paths.LogFile,
		Log:       logText,
	}
	if logErr != nil {
		data.LogError = logErr.Error()
	}
	if loadErr := s.Cron.LoadError(); loadErr != nil {
		data.StateError = loadErr.Error()
	}
	s.render(w, r, "cron", "计划任务", data)
}

func commandExists(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

func (s *Server) handleCronNew(w http.ResponseWriter, r *http.Request) {
	if err := s.Cron.LoadError(); err != nil {
		s.setFlash(w, "计划任务状态不可用: "+err.Error())
		s.redirect(w, r, "/cron")
		return
	}
	s.render(w, r, "cron_form", "新建计划任务", cronFormData{
		IsNew: true,
		Task: cronmgr.Task{
			Schedule: "0 3 * * *",
			Enabled:  true,
			Script:   "#!/bin/sh\nset -eu\n\n",
		},
	})
}

func (s *Server) handleCronCreate(w http.ResponseWriter, r *http.Request) {
	task := cronTaskFromForm(r)
	if _, err := s.Cron.Create(task.Name, task.Schedule, task.Script, task.Enabled); err != nil {
		s.render(w, r, "cron_form", "新建计划任务", cronFormData{
			Task: task, IsNew: true, Error: err.Error(),
		})
		return
	}
	s.setFlash(w, "计划任务已创建并同步到 Cron")
	s.redirect(w, r, "/cron")
}

func (s *Server) handleCronEdit(w http.ResponseWriter, r *http.Request) {
	task, ok := s.Cron.Get(r.PathValue("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	s.render(w, r, "cron_form", "编辑计划任务", cronFormData{Task: task})
}

func (s *Server) handleCronUpdate(w http.ResponseWriter, r *http.Request) {
	task := cronTaskFromForm(r)
	task.ID = r.PathValue("id")
	if err := s.Cron.Update(task.ID, task.Name, task.Schedule, task.Script, task.Enabled); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			http.NotFound(w, r)
			return
		}
		s.render(w, r, "cron_form", "编辑计划任务", cronFormData{
			Task: task, Error: err.Error(),
		})
		return
	}
	s.setFlash(w, "计划任务已保存并同步到 Cron")
	s.redirect(w, r, "/cron")
}

func cronTaskFromForm(r *http.Request) cronmgr.Task {
	return cronmgr.Task{
		Name:     r.FormValue("name"),
		Schedule: r.FormValue("schedule"),
		Script:   r.FormValue("script"),
		Enabled:  r.FormValue("enabled") == "on",
	}
}

func (s *Server) handleCronToggle(w http.ResponseWriter, r *http.Request) {
	enabled := r.FormValue("enabled") == "true"
	if err := s.Cron.SetEnabled(r.PathValue("id"), enabled); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			http.NotFound(w, r)
			return
		}
		s.setFlash(w, "切换计划任务失败: "+err.Error())
	} else if enabled {
		s.setFlash(w, "计划任务已启用")
	} else {
		s.setFlash(w, "计划任务已停用")
	}
	s.redirect(w, r, "/cron")
}

func (s *Server) handleCronRun(w http.ResponseWriter, r *http.Request) {
	if err := s.Cron.RunNow(r.PathValue("id")); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			http.NotFound(w, r)
			return
		}
		s.setFlash(w, "启动任务失败: "+err.Error())
	} else {
		s.setFlash(w, "任务已在后台启动，输出将写入执行日志")
	}
	s.redirect(w, r, "/cron")
}

func (s *Server) handleCronDelete(w http.ResponseWriter, r *http.Request) {
	if err := s.Cron.Delete(r.PathValue("id")); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			http.NotFound(w, r)
			return
		}
		s.setFlash(w, "删除任务失败: "+err.Error())
	} else {
		s.setFlash(w, "计划任务已删除")
	}
	s.redirect(w, r, "/cron")
}

func (s *Server) handleCronService(w http.ResponseWriter, r *http.Request) {
	action := r.FormValue("action")
	switch action {
	case "start":
		if err := sysd.Action("start", "cron"); err != nil {
			s.setFlash(w, "启动 Cron 失败: "+err.Error())
		} else {
			s.setFlash(w, "Cron 服务已启动")
		}
	case "enable":
		if err := sysd.Action("enable", "cron"); err != nil {
			s.setFlash(w, "开启 Cron 自启失败: "+err.Error())
		} else {
			s.setFlash(w, "Cron 已开启开机自启")
		}
	default:
		s.setFlash(w, "未知操作")
	}
	s.redirect(w, r, "/cron")
}
