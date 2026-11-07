package core

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestShutdownCancelsRequestsAndPersistsSourceState(t *testing.T) {
	directory := t.TempDir()
	engine, err := newNativeEngine(directory)
	if err != nil {
		t.Fatal(err)
	}
	engine.mu.Lock()
	engine.sourceRecords[sourceHongguo] = nativeSourceRecord{Running: true, Stage: "查找新剧", StartedAt: time.Now()}
	if err := engine.saveSourceRecordsLocked(); err != nil {
		engine.mu.Unlock()
		t.Fatal(err)
	}
	engine.mu.Unlock()

	ctx, cancel := context.WithTimeout(engine.requestRootContext(), time.Minute)
	defer cancel()
	if ctx.Err() != nil {
		t.Fatal("停机前请求上下文应可用")
	}

	nativeState.Lock()
	previous := nativeState.engine
	nativeState.engine = engine
	nativeState.Unlock()
	defer func() {
		nativeState.Lock()
		nativeState.engine = previous
		nativeState.Unlock()
	}()

	raw := NativeRequest(`{"action":"shutdown"}`)
	var response struct {
		OK   bool `json:"ok"`
		Data struct {
			Stopped bool `json:"stopped"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(raw), &response); err != nil {
		t.Fatal(err)
	}
	if !response.OK || !response.Data.Stopped {
		t.Fatalf("shutdown 应返回成功，实际 %s", raw)
	}
	if ctx.Err() == nil {
		t.Fatal("停机后在途请求上下文必须被取消")
	}

	body, err := os.ReadFile(filepath.Join(directory, "sources.json"))
	if err != nil {
		t.Fatal(err)
	}
	var records map[string]nativeSourceRecord
	if err := json.Unmarshal(body, &records); err != nil {
		t.Fatal(err)
	}
	if record := records[sourceHongguo]; record.Running {
		t.Fatal("停机后不应留下 running 状态")
	} else if record.Stage != "任务已中断" {
		t.Fatalf("停机后应有中断说明，实际 %q", record.Stage)
	}
}

func TestCoverHostBackoffSkipsRepeatedFailures(t *testing.T) {
	cache := &nativeCoverCache{
		pending:  map[string]*nativeCoverCall{},
		failures: map[string]int{},
		blocked:  map[string]time.Time{},
	}
	const host = "unreachable.example.test"
	if cache.coverHostBlocked(host) {
		t.Fatal("首次请求不应被跳过")
	}
	for index := 0; index < nativeCoverFailureThreshold; index++ {
		cache.observeCoverResult(host, false)
	}
	if !cache.coverHostBlocked(host) {
		t.Fatal("连续失败后应跳过同一域名的海报请求")
	}
	if cache.coverHostBlocked("healthy.example.test") {
		t.Fatal("熔断不应影响其它域名")
	}
	cache.observeCoverResult(host, true)
	if cache.coverHostBlocked(host) {
		t.Fatal("恢复成功后应立即解除熔断")
	}
	if got := nativeCoverHost("https://CDN.Example.Test:443/a.webp"); got != "cdn.example.test" {
		t.Fatalf("海报域名解析错误：%q", got)
	}
}

func TestCoverFetchStopsWithShutdownContext(t *testing.T) {
	directory := t.TempDir()
	engine, err := newNativeEngine(directory)
	if err != nil {
		t.Fatal(err)
	}
	cache := engine.covers
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cache.rootCtx = ctx
	call := &nativeCoverCall{done: make(chan struct{})}
	cache.pending["probe"] = call
	cache.fetch("probe", nativeDrama{ID: "probe", Source: sourceHongguo, Cover: "https://unreachable.example.test/a.webp"}, "", "", call)
	select {
	case <-call.done:
	default:
		t.Fatal("请求应立即结束")
	}
	if call.err == nil {
		t.Fatal("已取消的上下文应返回错误")
	}
}
