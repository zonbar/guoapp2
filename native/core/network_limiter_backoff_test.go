package core

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

func staleBackoff(limiter *requestLimiter, host string) {
	limiter.mu.Lock()
	limiter.backoffs[host] = &requestBackoff{host: host, status: http.StatusForbidden, until: time.Now().Add(time.Minute)}
	limiter.backgroundBackoffs[host] = &requestBackoff{host: host, status: http.StatusForbidden, until: time.Now().Add(time.Minute)}
	limiter.mu.Unlock()
}

func TestProxyChangeClearsRequestBackoffs(t *testing.T) {
	engine, err := newNativeEngine(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	limiter := engine.downloader.limiter
	const host = "blocked.example.test"
	// 先把出口固定为 direct，再注入退避，确保后续对比只针对「设置是否真的变化」。
	engine.applyResourceSettings(nativeResourceSettings{ProxyMode: "direct", CatalogConcurrency: 3, CatalogIntervalMS: 0, DownloadConcurrency: 2})
	staleBackoff(limiter, host)
	request, _ := http.NewRequest(http.MethodGet, "https://"+host+"/", nil)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	if _, err := limiter.acquire(ctx, request); err == nil {
		t.Fatal("退避期内普通请求应被拦截")
	}
	// 只调整并发与间隔，不应丢掉仍然有效的限流结论。
	engine.applyResourceSettings(nativeResourceSettings{ProxyMode: "direct", CatalogConcurrency: 2, CatalogIntervalMS: 0, DownloadConcurrency: 2})
	if _, err := limiter.acquire(ctx, request); err == nil {
		t.Fatal("仅调整并发不应清空退避")
	}
	// 切换网络出口后，旧出口下得到的 403 结论必须失效。
	engine.applyResourceSettings(nativeResourceSettings{ProxyMode: "auto", CatalogConcurrency: 2, CatalogIntervalMS: 0, DownloadConcurrency: 2})
	release, err := limiter.acquire(ctx, request)
	if err != nil {
		t.Fatalf("切换代理后应放行请求：%v", err)
	}
	release()
}

func TestForceRefreshBypassesRequestBackoffs(t *testing.T) {
	engine, err := newNativeEngine(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	limiter := engine.downloader.limiter
	const host = "blocked.example.test"
	staleBackoff(limiter, host)
	request, _ := http.NewRequest(http.MethodGet, "https://"+host+"/", nil)
	base := context.WithValue(context.Background(), backgroundCatalogKey{}, true)
	ctx, cancel := context.WithTimeout(base, 3*time.Second)
	defer cancel()

	if _, err := limiter.acquire(ctx, request); err == nil {
		t.Fatal("后台请求仍应受退避约束")
	}
	forceCtx := context.WithValue(ctx, forceRefreshKey{}, true)
	release, err := limiter.acquire(forceCtx, request)
	if err != nil {
		t.Fatalf("主动检测应绕开退避：%v", err)
	}
	release()
}

type forceRefreshProbeTransport struct {
	calls  int
	forced bool
}

func (transport *forceRefreshProbeTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	transport.calls++
	if value, _ := request.Context().Value(forceRefreshKey{}).(bool); value {
		transport.forced = true
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Status:     "200 OK",
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader("<!doctype html><html><body></body></html>")),
		Request:    request,
	}, nil
}

func TestCheckSourceRefreshesDespiteStaleBackoff(t *testing.T) {
	engine, err := newNativeEngine(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	transport := &forceRefreshProbeTransport{}
	engine.downloader.client.Transport = transport
	limiter := engine.downloader.limiter
	for _, candidate := range engine.downloader.providerURLCandidates(huaguoBaseURL + "/") {
		if parsed, err := url.Parse(candidate); err == nil {
			staleBackoff(limiter, parsed.Hostname())
		}
	}
	plain, _ := http.NewRequest(http.MethodGet, huaguoBaseURL+"/", nil)
	if _, err := limiter.acquire(context.Background(), plain); err == nil {
		t.Fatal("测试前置条件失败：普通请求未被退避拦截")
	}

	// 只跑「入口与目录」这一步；空页面会导致解析不到剧集，但请求必须真实发出。
	_ = engine.checkSource(context.Background(), sourceHuaguo, nativeDrama{}, false)
	if transport.calls == 0 {
		t.Fatal("主动检测被旧退避挡住，没有发出任何请求")
	}
	if !transport.forced {
		t.Fatal("主动检测的请求缺少强制刷新标记")
	}
}
