package web

// 后台任务状态聚合（供前端「后台」页展示倒计时）。

import (
	"net/http"
	"time"

	"mmbot/internal/strm"
	"mmbot/internal/transfer"
)

// rfc3339OrNil 时间序列化：零值返回 null。
func rfc3339OrNil(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.Format(time.RFC3339)
}

// handleTasksStatus 后台任务状态：频道监控 / 定时整理 / 全量同步 STRM。
func (s *Server) handleTasksStatus(w http.ResponseWriter, r *http.Request) {
	channel := map[string]any{"scanning": false, "active": false, "last_check": nil, "next_check": nil}
	if s.bot != nil {
		scanning, active, last, next := s.bot.MonitorStatus()
		channel = map[string]any{
			"scanning":   scanning,
			"active":     active,
			"last_check": rfc3339OrNil(last),
			"next_check": rfc3339OrNil(next),
		}
	}

	tf := map[string]any{"scheduler_running": false, "scanning": false, "scan_interval_min": 0, "next_run": nil}
	if sch := transfer.GetTransferScheduler(); sch != nil {
		tf = map[string]any{
			"scheduler_running": sch.IsRunning(),
			"scanning":          sch.IsScanning(),
			"scan_interval_min": sch.ScanInterval(),
			"next_run":          rfc3339OrNil(sch.NextRun()),
		}
	}

	strmStatus := map[string]any{"enabled": false, "busy": false, "full_sync_enabled": false, "full_sync_cron": "", "next_run": nil}
	if rt := strm.GetRuntime(); rt != nil {
		st := rt.Status()
		strmStatus = map[string]any{
			"enabled":           st["enabled"],
			"busy":              st["busy"],
			"full_sync_enabled": st["full_sync_enabled"],
			"full_sync_cron":    st["full_sync_cron"],
			"next_run":          st["next_full_sync"],
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"channel_monitor": channel,
		"transfer":        tf,
		"strm_full_sync":  strmStatus,
	})
}

// handleMonitorTrigger 手动触发一次频道监控检查。
func (s *Server) handleMonitorTrigger(w http.ResponseWriter, r *http.Request) {
	if s.bot == nil {
		writeJSON(w, http.StatusOK, map[string]any{"started": false, "message": "频道监控未初始化"})
		return
	}
	started := s.bot.TriggerCheck()
	resp := map[string]any{"started": started}
	if !started {
		resp["message"] = "已有扫描在进行中，请稍后再试"
	}
	writeJSON(w, http.StatusOK, resp)
}
