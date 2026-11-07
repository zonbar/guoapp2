package core

import (
	"context"
	"time"
)

// requestRootContext 是引擎内所有在途请求的共同父上下文。应用退出时取消它，
// 能让仍挂在 Dart isolate 里的原生调用立即返回，不必等各自的超时。
func (engine *nativeEngine) requestRootContext() context.Context {
	if engine.rootCtx != nil {
		return engine.rootCtx
	}
	return context.Background()
}

// shutdown 在进程退出前停止后台任务、落盘站源状态并取消在途网络请求。
// 由 macOS 应用退出流程通过 DuanjuRequest({"action":"shutdown"}) 同步调用。
func (engine *nativeEngine) shutdown() {
	if engine.rootCancel != nil {
		engine.rootCancel()
	}
	engine.mu.Lock()
	defer engine.mu.Unlock()
	for _, task := range engine.sourceTasks {
		if task != nil && task.cancel != nil {
			task.cancel()
		}
	}
	for source, record := range engine.sourceRecords {
		if !record.Running {
			continue
		}
		record.Running = false
		record.Stage = "任务已中断"
		record.Error = "应用已退出，已保留更新内容，可重新开始"
		record.FinishedAt = time.Now()
		engine.sourceRecords[source] = record
	}
	_ = engine.saveSourceRecordsLocked()
}
