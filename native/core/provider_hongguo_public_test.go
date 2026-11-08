package core

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const (
	hongguoPublicTestSeries = "7701935931787316286"
	hongguoPublicTestVideo  = "7701937936731737112"
	hongguoPublicTestMedia  = "https://v26-hgweb.qznovelvod.com/video/tos/sample.mp4?a=8662"
)

func hongguoPublicTestDownloader(t *testing.T, handler http.HandlerFunc) (*Downloader, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	d := &Downloader{
		cfg:           Config{dataDir: t.TempDir(), Retries: 1, PageSize: 30, MaxPagesPerSort: 1, HongguoURL: server.URL},
		client:        server.Client(),
		limiter:       newRequestLimiter(3, 0),
		providerHosts: map[string]string{},
	}
	return d, server
}

func TestHongguoPublicFallbackResolvesRequestedEpisode(t *testing.T) {
	var gotBody map[string]string
	var gotPath, gotMethod, gotOrigin, gotReferer, gotContentType string
	d, server := hongguoPublicTestDownloader(t, func(writer http.ResponseWriter, request *http.Request) {
		gotPath, gotMethod = request.URL.Path, request.Method
		gotOrigin = request.Header.Get("Origin")
		gotReferer = request.Header.Get("Referer")
		gotContentType = request.Header.Get("Content-Type")
		_ = json.NewDecoder(request.Body).Decode(&gotBody)
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"main_url":"` + hongguoPublicTestMedia + `","url_expire":1794168591,"base_resp":{"StatusCode":0,"StatusMessage":""}}`))
	})
	_ = server

	media, err := d.resolveHongguoPublicFallback(context.Background(), hongguoPublicTestSeries, hongguoPublicTestVideo)
	if err != nil {
		t.Fatalf("公开回退取流失败: %v", err)
	}
	if media.URL != hongguoPublicTestMedia {
		t.Fatalf("媒体地址 = %q, want %q", media.URL, hongguoPublicTestMedia)
	}
	if media.Referer != d.providerBaseURL(sourceHongguo)+"/" {
		t.Fatalf("Referer = %q", media.Referer)
	}
	if gotPath != hongguoPublicFallbackPath || gotMethod != http.MethodPost {
		t.Fatalf("请求 = %s %s", gotMethod, gotPath)
	}
	if gotContentType != "application/json" || gotOrigin != d.providerBaseURL(sourceHongguo) {
		t.Fatalf("headers: content-type=%q origin=%q", gotContentType, gotOrigin)
	}
	wantReferer := d.providerBaseURL(sourceHongguo) + "/player/" + hongguoPublicTestSeries + "/" + hongguoPublicTestVideo
	if gotReferer != wantReferer {
		t.Fatalf("Referer = %q, want %q", gotReferer, wantReferer)
	}
	// 关键：绝不能把其它分集（例如免费试看集）的 ID 发给接口。
	if len(gotBody) != 1 || gotBody["item_id"] != hongguoPublicTestVideo {
		t.Fatalf("请求体 = %v, 必须只含目标集 item_id", gotBody)
	}
}

func TestHongguoPublicFallbackUnavailableDoesNotBackoffSite(t *testing.T) {
	d, _ := hongguoPublicTestDownloader(t, func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusForbidden)
		_, _ = writer.Write([]byte(`{"error":"public_fallback_unavailable"}`))
	})

	_, err := d.resolveHongguoPublicFallback(context.Background(), hongguoPublicTestSeries, hongguoPublicTestVideo)
	if !errors.Is(err, errHongguoPublicEpisodeUnavailable) {
		t.Fatalf("err = %v, want errHongguoPublicEpisodeUnavailable", err)
	}
	// 403 是分集授权结果，不能让同站目录/封面请求进入退避。
	d.limiter.mu.Lock()
	backoffCount := len(d.limiter.backoffs) + len(d.limiter.backgroundBackoffs)
	d.limiter.mu.Unlock()
	if backoffCount != 0 {
		t.Fatalf("403 触发了 %d 个整站退避", backoffCount)
	}
}

func TestHongguoPublicFallbackRejectsBadPayloads(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
	}{
		{"业务错误体", http.StatusOK, `{"error":"public_fallback_unavailable"}`},
		{"空地址", http.StatusOK, `{"main_url":"","base_resp":{"StatusCode":0}}`},
		{"非 HTTP 地址", http.StatusOK, `{"main_url":"file:///tmp/x.mp4","base_resp":{"StatusCode":0}}`},
		{"非 JSON", http.StatusOK, `not-json`},
		{"服务状态码非 0", http.StatusOK, `{"main_url":"` + hongguoPublicTestMedia + `","base_resp":{"StatusCode":1001}}`},
		{"服务端 502", http.StatusBadGateway, `{"error":"oops"}`},
		{"401 未授权", http.StatusUnauthorized, `{}`},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			d, _ := hongguoPublicTestDownloader(t, func(writer http.ResponseWriter, request *http.Request) {
				writer.Header().Set("Content-Type", "application/json")
				writer.WriteHeader(item.status)
				_, _ = writer.Write([]byte(item.body))
			})
			if _, err := d.resolveHongguoPublicFallback(context.Background(), hongguoPublicTestSeries, hongguoPublicTestVideo); err == nil {
				t.Fatal("无效响应被当成了可用媒体地址")
			}
		})
	}
}

func TestHongguoPublicFallbackRejectsInvalidIDs(t *testing.T) {
	calls := 0
	d, _ := hongguoPublicTestDownloader(t, func(writer http.ResponseWriter, request *http.Request) {
		calls++
		writer.WriteHeader(http.StatusOK)
	})
	for _, pair := range [][2]string{{"", hongguoPublicTestVideo}, {"abc", hongguoPublicTestVideo}, {hongguoPublicTestSeries, ""}, {hongguoPublicTestSeries, "../evil"}} {
		if _, err := d.resolveHongguoPublicFallback(context.Background(), pair[0], pair[1]); err == nil {
			t.Fatalf("非法 ID %v 被接受", pair)
		}
	}
	if calls != 0 {
		t.Fatalf("非法 ID 仍发出了 %d 次请求", calls)
	}
}

func TestHongguoPublicFallbackParserKeepsHostOnly(t *testing.T) {
	media, err := parseHongguoPublicFallback([]byte(`{"main_url":"https://cdn.example.com/a.mp4"}`), "https://hongguoduanju.com")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !strings.HasPrefix(media.URL, "https://cdn.example.com/") || media.Referer != "https://hongguoduanju.com/" {
		t.Fatalf("media = %+v", media)
	}
}

// 端到端：App 接口空响应 + 播放页脱敏（protectedPlayback）时，必须改由
// 网页公开回退接口取到该集地址，而不是直接报「受保护内容」。
func TestHongguoMediaUsesPublicFallbackWhenPageIsRedacted(t *testing.T) {
	var publicCalls int
	handler := func(writer http.ResponseWriter, request *http.Request) {
		switch {
		case request.URL.Path == hongguoPublicFallbackPath:
			publicCalls++
			var body map[string]string
			_ = json.NewDecoder(request.Body).Decode(&body)
			if body["item_id"] != hongguoPublicTestVideo {
				t.Errorf("公开回退请求了错误的分集: %v", body)
			}
			writer.Header().Set("Content-Type", "application/json")
			_, _ = writer.Write([]byte(`{"main_url":"` + hongguoPublicTestMedia + `","base_resp":{"StatusCode":0}}`))
		case strings.HasPrefix(request.URL.Path, "/player/"):
			writer.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = writer.Write([]byte(`<html><script>window._ROUTER_DATA = {"loaderData":{"player_page":{"protectedPlayback":true,"videoList":{},"vid":"` + hongguoPublicTestVideo + `","series_id":"` + hongguoPublicTestSeries + `"}}}</script></html>`))
		case strings.HasPrefix(request.URL.Path, "/novel/player/"):
			// 站点风控：HTTP 200 + 空 body
			writer.Header().Set("Content-Type", "application/json")
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}
	d, server := hongguoPublicTestDownloader(t, handler)
	d.providerHosts[sourceHongguo] = server.URL
	d.hongguoClient().baseURL = server.URL

	task := Task{
		DramaID: providerDramaID(sourceHongguo, hongguoPublicTestSeries),
		Chapter: Chapter{Source: sourceHongguo, VideoURL: "hongguo-cenc://" + hongguoPublicTestVideo},
	}
	media, err := d.resolveHongguoMedia(context.Background(), task)
	if err != nil {
		t.Fatalf("编排未走公开回退: %v", err)
	}
	if media.URL != hongguoPublicTestMedia {
		t.Fatalf("media.URL = %q", media.URL)
	}
	if publicCalls != 1 {
		t.Fatalf("公开回退接口调用次数 = %d, want 1", publicCalls)
	}
}
