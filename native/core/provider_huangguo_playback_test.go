package core

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// 新版播放页：初始化数据里播放地址为空，播放器容器改为 cyberPlayer。
const huangguoAIPlaybackPage = `<html><head><script id="videoInitialData" type="application/json">` +
	`{"id":"10586","ep":1,"videoSrc":"","epPlaySrcs":{},"cdnM3u8Enabled":true,"cdnAuthKey":"x-cdn-auth"}` +
	`</script></head><body><div id="cyberPlayer"></div></body></html>`

func huangguoAIPlaybackResponseBody(t *testing.T, origin string) []byte {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"status": 1,
		"msg":    "",
		"data": map[string]any{
			"manifest_method": "GET",
			"streams":         map[string]any{"main": origin + "/hls/master.m3u8?auth_key=fixture"},
			"cdn": map[string]any{
				"m3u8Enabled": true,
				"authKey":     "x-cdn-auth",
				"authValue":   "fixture-token",
				"app":         "150",
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

// 站源会把解析到的 http 地址统一升级为 https，因此夹具使用 TLS 服务器。
func huangguoAITestDownloader(t *testing.T, handler http.HandlerFunc) (*Downloader, *httptest.Server) {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	return &Downloader{
		cfg:           Config{dataDir: t.TempDir(), Retries: 1, PageSize: 30, MaxPagesPerSort: 1},
		client:        server.Client(),
		limiter:       newRequestLimiter(3, 0),
		providerHosts: map[string]string{},
	}, server
}

func huangguoAITestTask(pageURL string) Task {
	return Task{
		DramaID: "huangguoai:10586",
		Chapter: Chapter{
			ID:             "huangguoai:10586:1",
			Source:         sourceHuangguoAI,
			Title:          "第1集",
			PageURL:        pageURL,
			CurrentEpisode: rawEpisode(1),
		},
		Index: 1,
	}
}

func TestParseHuangguoAIPlaybackExtractsSignedManifest(t *testing.T) {
	page := "https://huangguoai.com/video/10586/ep-2/"
	result, err := parseHuangguoAIPlayback(huangguoAIPlaybackResponseBody(t, "https://huangguoai.com"), page)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.URL, "/hls/master.m3u8?auth_key=fixture") {
		t.Fatal("missing signed manifest", result.URL)
	}
	if result.CDNHeaders["X-Cdn-Auth"] != "fixture-token" || result.CDNHeaders["X-Cdn-App"] != "150" {
		t.Fatal("missing cdn headers", result.CDNHeaders)
	}
}

// 站点实际返回体比最小样例多出图片 CDN、预览流、时间戳等字段；地址已脱敏。
func TestParseHuangguoAIPlaybackAcceptsLiveResponseShape(t *testing.T) {
	payload := `{
		"status": 1, "msg": "", "req_time": 1793994253,
		"data": {
			"mode": 0, "timestamp": 1794001500, "manifest_method": "GET",
			"id": 10586, "ep": 1, "media_id": "",
			"cdn": {
				"m3u8Enabled": true, "otherEnabled": false,
				"authKey": "x-cdn-auth", "authValue": "fixture-token", "app": "150",
				"imgEnabled": false, "imgAuthKey": "x-cdn-auth", "imgAuthValue": "fixture-image-token"
			},
			"streams": {
				"main": "https://cdn.example.test/videos5/abc/abc.m3u8?auth_key=fixture",
				"hevc": "https://cdn.example.test/m3m/abc.m3u8",
				"preview": "https://cdn.example.test/videos5/abc/abc.m3u8?auth_key=fixture"
			}
		}
	}`
	result, err := parseHuangguoAIPlayback([]byte(payload), "https://huangguoai.com/video/10586/")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.URL, "cdn.example.test/videos5/abc/abc.m3u8") {
		t.Fatal("live-shaped manifest was not accepted", result.URL)
	}
	if len(result.CDNHeaders) != 2 || result.CDNHeaders["X-Cdn-Auth"] != "fixture-token" {
		t.Fatal("live-shaped cdn headers were not accepted", result.CDNHeaders)
	}
}

func TestParseHuangguoAIPlaybackRejectsBadPayloads(t *testing.T) {
	page := "https://huangguoai.com/video/10586/"
	cases := []struct {
		name    string
		payload string
		want    string
	}{
		{"invalid json", `not-json`, "无法识别"},
		{"rejected", `{"status":0,"msg":"登录后可观看","data":{}}`, "登录后可观看"},
		{"unsupported method", `{"status":1,"data":{"manifest_method":"POST","streams":{"main":"https://cdn.test/a.m3u8"}}}`, "不支持的取流方式"},
		{"empty manifest", `{"status":1,"data":{"manifest_method":"GET","streams":{"main":""}}}`, "未返回有效的播放清单"},
		{"web page as manifest", `{"status":1,"data":{"manifest_method":"GET","streams":{"main":"https://cdn.test/detail.html"}}}`, "未返回有效的播放清单"},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			_, err := parseHuangguoAIPlayback([]byte(item.payload), page)
			if err == nil || !strings.Contains(err.Error(), item.want) {
				t.Fatalf("expected error containing %q, got %v", item.want, err)
			}
		})
	}
	// 非法的头部名称不能污染请求头集合。
	result, err := parseHuangguoAIPlayback([]byte(`{"status":1,"data":{"manifest_method":"GET",`+
		`"streams":{"main":"https://cdn.test/a.m3u8"},"cdn":{"m3u8Enabled":true,`+
		`"authKey":"x-cdn-auth: injected","authValue":"token","app":"150"}}}`), page)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.CDNHeaders) != 0 {
		t.Fatal("unsafe header name was accepted", result.CDNHeaders)
	}
}

func TestHuangguoAIPlaybackEpisodeAndVideoID(t *testing.T) {
	if number := huangguoAIEpisodeNumber("https://huangguoai.com/video/10586/ep-7/", Chapter{}, 1); number != 7 {
		t.Fatal("episode path should win", number)
	}
	if number := huangguoAIEpisodeNumber("https://huangguoai.com/video/10586/", Chapter{CurrentEpisode: rawEpisode(3)}, 1); number != 3 {
		t.Fatal("chapter episode should be used", number)
	}
	if number := huangguoAIEpisodeNumber("https://huangguoai.com/video/10586/", Chapter{}, 5); number != 5 {
		t.Fatal("fallback should be used", number)
	}
	if id := huangguoAIVideoID("https://huangguoai.com/video/10586/ep-2/"); id != "10586" {
		t.Fatal("video id", id)
	}
	if id := huangguoAIVideoID("https://huangguoai.com/series/not-a-number/"); id != "" {
		t.Fatal("non numeric id should be rejected", id)
	}
}

func TestResolveHuangguoAIMediaFallsBackToPlaybackAPI(t *testing.T) {
	var posts, manifests atomic.Int32
	d, server := huangguoAITestDownloader(t, func(writer http.ResponseWriter, request *http.Request) {
		origin := "https://" + request.Host
		switch {
		case request.Method == http.MethodGet && strings.HasPrefix(request.URL.Path, "/video/"):
			writer.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprint(writer, huangguoAIPlaybackPage)
		case request.Method == http.MethodPost && strings.HasSuffix(request.URL.Path, "/playback"):
			posts.Add(1)
			if request.URL.Path != "/api/videos/10586/episodes/1/playback" {
				t.Errorf("unexpected playback path %q", request.URL.Path)
			}
			if request.Header.Get("Content-Type") != "application/json" || request.Header.Get("Origin") != origin {
				t.Errorf("missing playback headers: %v", request.Header)
			}
			body, _ := io.ReadAll(request.Body)
			if strings.TrimSpace(string(body)) != "{}" {
				t.Errorf("unexpected playback body %q", body)
			}
			writer.Header().Set("Content-Type", "application/json")
			writer.Write(huangguoAIPlaybackResponseBody(t, origin))
		case request.Method == http.MethodGet && strings.HasSuffix(request.URL.Path, ".m3u8"):
			manifests.Add(1)
			if request.Header.Get("X-Cdn-Auth") != "fixture-token" || request.Header.Get("X-Cdn-App") != "150" {
				t.Errorf("cdn headers missing on manifest request: %v", request.Header)
			}
			fmt.Fprint(writer, "#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-ENDLIST\n")
		default:
			http.NotFound(writer, request)
		}
	})
	media, err := d.resolveProviderMedia(context.Background(), huangguoAITestTask(server.URL+"/video/10586/"))
	if err != nil {
		t.Fatal(err)
	}
	if posts.Load() != 1 || manifests.Load() != 1 {
		t.Fatalf("unexpected request counts: posts=%d manifests=%d", posts.Load(), manifests.Load())
	}
	if !strings.Contains(media.URL, "/hls/master.m3u8") || !strings.HasPrefix(media.Playlist, "#EXTM3U") {
		t.Fatal("playlist was not resolved", media.URL, media.Playlist)
	}
	if media.credentials == nil || len(media.credentials.cdnHeaders) != 2 {
		t.Fatal("cdn credentials were not carried into media")
	}
}

func TestResolveHuangguoAIMediaPrefersInlineSource(t *testing.T) {
	var posts atomic.Int32
	d, server := huangguoAITestDownloader(t, func(writer http.ResponseWriter, request *http.Request) {
		origin := "https://" + request.Host
		switch {
		case request.Method == http.MethodGet && strings.HasPrefix(request.URL.Path, "/video/"):
			fmt.Fprintf(writer, `<div data-play-src="%s/inline.m3u8"></div>`, origin)
		case request.Method == http.MethodPost && strings.HasSuffix(request.URL.Path, "/playback"):
			posts.Add(1)
			http.Error(writer, "should not be called", http.StatusForbidden)
		case request.Method == http.MethodGet && strings.HasSuffix(request.URL.Path, ".m3u8"):
			fmt.Fprint(writer, "#EXTM3U\n#EXT-X-ENDLIST\n")
		default:
			http.NotFound(writer, request)
		}
	})
	media, err := d.resolveProviderMedia(context.Background(), huangguoAITestTask(server.URL+"/video/10586/"))
	if err != nil {
		t.Fatal(err)
	}
	if posts.Load() != 0 {
		t.Fatal("inline source should not trigger the playback API")
	}
	if !strings.HasSuffix(media.URL, "/inline.m3u8") || media.credentials != nil {
		t.Fatal("inline playback changed", media.URL, media.credentials)
	}
}

func TestResolveHuangguoAIMediaReportsPlaybackFailure(t *testing.T) {
	d, server := huangguoAITestDownloader(t, func(writer http.ResponseWriter, request *http.Request) {
		switch {
		case request.Method == http.MethodGet && strings.HasPrefix(request.URL.Path, "/video/"):
			fmt.Fprint(writer, huangguoAIPlaybackPage)
		case request.Method == http.MethodPost && strings.HasSuffix(request.URL.Path, "/playback"):
			http.Error(writer, "blocked", http.StatusForbidden)
		default:
			http.NotFound(writer, request)
		}
	})
	_, err := d.resolveProviderMedia(context.Background(), huangguoAITestTask(server.URL+"/video/10586/"))
	if err == nil || !strings.Contains(err.Error(), "播放取址失败") {
		t.Fatalf("expected a diagnosable playback error, got %v", err)
	}
	if strings.Contains(err.Error(), "请刷新章节") {
		t.Fatal("error still points at chapter refresh", err)
	}
}

func TestCDNHeadersOnlyAppliedToManifestRequests(t *testing.T) {
	credentials := &providerMediaCredentials{cdnHeaders: map[string]string{"X-Cdn-Auth": "token", "X-Cdn-App": "150"}}
	manifest, err := http.NewRequest(http.MethodGet, "https://cdn.test/live/index.m3u8?auth_key=1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := credentials.apply(manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Header.Get("X-Cdn-Auth") != "token" || manifest.Header.Get("X-Cdn-App") != "150" {
		t.Fatal("manifest headers were not applied", manifest.Header)
	}
	segment, err := http.NewRequest(http.MethodGet, "https://cdn.test/live/segment.ts", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := credentials.apply(segment); err != nil {
		t.Fatal(err)
	}
	if segment.Header.Get("X-Cdn-Auth") != "" || segment.Header.Get("X-Cdn-App") != "" {
		t.Fatal("cdn headers leaked to segment requests", segment.Header)
	}
}
