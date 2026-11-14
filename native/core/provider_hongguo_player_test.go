package core

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	hongguoPlayerTestVideo     = "7703411040372919320"
	hongguoPlayerTestMedia     = "https://v5-se-gddgtc-reading-video.fqnovelvod.com/video/tos/sample.mp4?a=1967"
	hongguoPlayerTestSpade     = "nbwTwFiPEsNDhT7McIAh+nGyP/h1szj+QrUN/XK3Ds1xtjm+vg=="
	hongguoPlayerTestSeed      = "6y/DJ+M6iCbVImI1VhvkKiwZcAVDAIEoI8w5YLG0LW0="
	hongguoPlayerTestSpadeURL  = "qAABAHZQvWLT6TiIY/JepI4+dmwCiOsmjg9agWoCXLgAUV6D1nL4gq/tIv5RYCiJKq+esM0hKwtT3rxAdilEsELooqI="
	hongguoPlayerTestDeviceID  = "4140140237696298"
	hongguoPlayerTestInstallID = "4140140237700394"
)

const hongguoPlayerTestRegisterResponse = `{"code":0,"device_id":4140140237696298,"install_id":4140140237700394,"device_id_str":"4140140237696298","install_id_str":"4140140237700394"}`

func hongguoPlayerTestModel() string {
	definitions := []struct {
		name          string
		width, height int
	}{
		{"720p", 720, 1280}, {"540p", 540, 960}, {"480p", 480, 854}, {"360p", 360, 640},
	}
	rows := make([]map[string]any, 0, len(definitions))
	for _, item := range definitions {
		rows = append(rows, map[string]any{
			"definition": item.name, "vwidth": item.width, "vheight": item.height,
			"codec_type": "bytevc1", "spade_a": hongguoPlayerTestSpade,
			"main_url": base64.StdEncoding.EncodeToString([]byte(hongguoPlayerTestMedia + "&d=" + item.name)),
		})
	}
	raw, _ := json.Marshal(map[string]any{
		"status": 10, "video_duration": 141.571, "video_list": rows, "key_seed": hongguoPlayerTestSeed,
	})
	return string(raw)
}

func hongguoPlayerTestResponse() []byte {
	raw, _ := json.Marshal(map[string]any{
		"code": 0, "message": "请求成功",
		"BaseResp": map[string]any{"StatusCode": 0, "StatusMessage": "success"},
		"data":     map[string]any{"video_model": hongguoPlayerTestModel()},
	})
	return raw
}

type hongguoPlayerTestState struct {
	mu              sync.Mutex
	registerCalls   int
	shortplayCalls  int
	primaryCalls    int
	signHeaders     map[string]string
	requestedVideo  string
	contentEncoding string
	gzipBody        []byte
}

func (state *hongguoPlayerTestState) snapshot() hongguoPlayerTestState {
	state.mu.Lock()
	defer state.mu.Unlock()
	return hongguoPlayerTestState{
		registerCalls: state.registerCalls, shortplayCalls: state.shortplayCalls,
		primaryCalls: state.primaryCalls, requestedVideo: state.requestedVideo,
		contentEncoding: state.contentEncoding, gzipBody: append([]byte(nil), state.gzipBody...),
		signHeaders: state.signHeaders,
	}
}

func hongguoPlayerTestHandler(t *testing.T, state *hongguoPlayerTestState, shortplayStatus int, shortplayBody func() []byte) http.HandlerFunc {
	t.Helper()
	return func(writer http.ResponseWriter, request *http.Request) {
		state.mu.Lock()
		switch request.URL.Path {
		case "/device_register":
			state.registerCalls++
		case "/shortplay":
			state.shortplayCalls++
			body, _ := io.ReadAll(request.Body)
			var payload map[string]any
			_ = json.Unmarshal(body, &payload)
			state.requestedVideo, _ = payload["video_id"].(string)
			state.signHeaders = map[string]string{
				"X-Argus": request.Header.Get("X-Argus"), "X-Ladon": request.Header.Get("X-Ladon"),
				"X-Khronos": request.Header.Get("X-Khronos"), "X-SS-Stub": request.Header.Get("X-SS-Stub"),
				"Cookie": request.Header.Get("Cookie"), "User-Agent": request.Header.Get("User-Agent"),
			}
		case "/player_api", "/player_api_alt":
			state.primaryCalls++
			state.contentEncoding = request.Header.Get("Content-Encoding")
			body, _ := io.ReadAll(request.Body)
			state.gzipBody = append([]byte(nil), body...)
		}
		state.mu.Unlock()
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/device_register":
			writer.Header().Add("Set-Cookie", "store-region=cn-gd; Path=/")
			writer.Header().Add("Set-Cookie", "store-region-src=did; Path=/")
			writer.Header().Add("Set-Cookie", "install_id="+hongguoPlayerTestInstallID+"; Path=/")
			_, _ = writer.Write([]byte(hongguoPlayerTestRegisterResponse))
		case "/shortplay":
			if shortplayStatus != http.StatusOK {
				writer.WriteHeader(shortplayStatus)
				return
			}
			_, _ = writer.Write(shortplayBody())
		case "/player_api", "/player_api_alt":
			_, _ = writer.Write(shortplayBody())
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}
}

func hongguoPlayerTestDownloader(t *testing.T, handler http.HandlerFunc, dataDir string) (*Downloader, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	d := &Downloader{
		cfg: Config{
			dataDir: dataDir, Retries: 1, PageSize: 30, MaxPagesPerSort: 1,
			HongguoRegisterURL:  server.URL + "/device_register",
			HongguoShortplayURL: server.URL + "/shortplay",
			HongguoPlayerURL:    server.URL + "/player_api",
			HongguoPlayerAltURL: server.URL + "/player_api_alt",
		},
		client:        server.Client(),
		limiter:       newRequestLimiter(3, 0),
		providerHosts: map[string]string{},
	}
	return d, server
}

func TestHongguoPlayerResolvesFullEpisode(t *testing.T) {
	state := &hongguoPlayerTestState{}
	d, _ := hongguoPlayerTestDownloader(t, hongguoPlayerTestHandler(t, state, http.StatusOK, hongguoPlayerTestResponse), t.TempDir())
	media, err := d.resolveHongguoPlayerMedia(context.Background(), hongguoPlayerTestVideo)
	if err != nil {
		t.Fatalf("官方播放线路取流失败: %v", err)
	}
	if !strings.HasPrefix(media.URL, hongguoPlayerTestMedia) {
		t.Fatalf("media.URL = %q", media.URL)
	}
	if got := hex.EncodeToString(media.CENCKey); got != "0f493590f2659b72732455a470f2caee" {
		t.Fatalf("CENCKey = %s", got)
	}
	if media.Quality != 720 || len(media.Variants) != 4 {
		t.Fatalf("清晰度 = %d, 备选数 = %d, want 720/4", media.Quality, len(media.Variants))
	}
	if media.Referer != "https://novel.snssdk.com/" {
		t.Fatalf("Referer = %q", media.Referer)
	}
	snapshot := state.snapshot()
	if snapshot.registerCalls != 1 || snapshot.shortplayCalls != 1 {
		t.Fatalf("注册 %d 次, 取流 %d 次, want 1/1", snapshot.registerCalls, snapshot.shortplayCalls)
	}
	if snapshot.requestedVideo != hongguoPlayerTestVideo {
		t.Fatalf("取流请求了错误的分集: %q", snapshot.requestedVideo)
	}
	for _, key := range []string{"X-Argus", "X-Ladon", "X-Khronos", "X-SS-Stub", "Cookie"} {
		if snapshot.signHeaders[key] == "" {
			t.Fatalf("缺少签名头 %s: %v", key, snapshot.signHeaders)
		}
	}
	if !strings.Contains(snapshot.signHeaders["Cookie"], "install_id="+hongguoPlayerTestInstallID) {
		t.Fatalf("设备 cookie 未携带: %q", snapshot.signHeaders["Cookie"])
	}
}

func TestHongguoPlayerFallsBackToSignedGzipRoute(t *testing.T) {
	state := &hongguoPlayerTestState{}
	d, _ := hongguoPlayerTestDownloader(t, hongguoPlayerTestHandler(t, state, http.StatusNoContent, hongguoPlayerTestResponse), t.TempDir())
	media, err := d.resolveHongguoPlayerMedia(context.Background(), hongguoPlayerTestVideo)
	if err != nil {
		t.Fatalf("备用线路取流失败: %v", err)
	}
	if !strings.HasPrefix(media.URL, hongguoPlayerTestMedia) {
		t.Fatalf("media.URL = %q", media.URL)
	}
	snapshot := state.snapshot()
	if snapshot.primaryCalls != 1 || snapshot.contentEncoding != "gzip" {
		t.Fatalf("备用线路调用 %d 次, Content-Encoding=%q", snapshot.primaryCalls, snapshot.contentEncoding)
	}
	reader, err := gzip.NewReader(bytes.NewReader(snapshot.gzipBody))
	if err != nil {
		t.Fatalf("备用线路请求体不是 gzip: %v", err)
	}
	defer reader.Close()
	body, err := io.ReadAll(reader)
	if err != nil || !strings.Contains(string(body), "mixed_video_id_map") {
		t.Fatalf("备用线路请求体无效: %s, %v", string(body), err)
	}
}

func TestHongguoPlayerReusesRegisteredDevice(t *testing.T) {
	state := &hongguoPlayerTestState{}
	dir := t.TempDir()
	d, _ := hongguoPlayerTestDownloader(t, hongguoPlayerTestHandler(t, state, http.StatusOK, hongguoPlayerTestResponse), dir)
	for i := 0; i < 2; i++ {
		if _, err := d.resolveHongguoPlayerMedia(context.Background(), hongguoPlayerTestVideo); err != nil {
			t.Fatalf("第 %d 次取流失败: %v", i+1, err)
		}
	}
	if state.snapshot().registerCalls != 1 {
		t.Fatalf("同一实例重复注册 %d 次", state.snapshot().registerCalls)
	}
	restarted, _ := hongguoPlayerTestDownloader(t, hongguoPlayerTestHandler(t, state, http.StatusOK, hongguoPlayerTestResponse), dir)
	if _, err := restarted.resolveHongguoPlayerMedia(context.Background(), hongguoPlayerTestVideo); err != nil {
		t.Fatalf("重启后取流失败: %v", err)
	}
	if state.snapshot().registerCalls != 1 {
		t.Fatalf("持久化设备未被复用，注册次数 = %d", state.snapshot().registerCalls)
	}
}

func hongguoPlayerShortplayMapResponse(fallback string) []byte {
	variants := map[string]any{}
	for index, definition := range []struct {
		name          string
		width, height int
	}{{"540p", 540, 960}, {"720p", 720, 1280}, {"360p", 360, 640}} {
		variants[fmt.Sprintf("video_%d", index+1)] = map[string]any{
			"definition": definition.name, "vwidth": definition.width, "vheight": definition.height,
			"codec_type": "bytevc1", "encrypt": true, "spade_a": hongguoPlayerTestSpade,
			"main_url": base64.StdEncoding.EncodeToString([]byte(hongguoPlayerTestMedia + "&d=" + definition.name)),
		}
	}
	model, _ := json.Marshal(map[string]any{
		"status": 10, "video_duration": 141.571, "video_list": variants,
		"key_seed":     hongguoPlayerTestSeed,
		"fallback_api": map[string]any{"fallback_api": fallback},
	})
	raw, _ := json.Marshal(map[string]any{"code": 0, "data": map[string]any{"video_model": string(model)}})
	return raw
}

// 番茄通道的 video_list 是 video_1..N 组成的对象，曾被误判为无内联片源而
// 去请求 fallback_api，导致拿到不带 spade_a 的变体、返回无密钥的 CENC 片源。
func TestHongguoPlayerMapVideoListKeepsContentKey(t *testing.T) {
	var mu sync.Mutex
	var fallbackCalls int
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/device_register":
			writer.Header().Add("Set-Cookie", "install_id="+hongguoPlayerTestInstallID+"; Path=/")
			_, _ = writer.Write([]byte(hongguoPlayerTestRegisterResponse))
		case "/shortplay":
			_, _ = writer.Write(hongguoPlayerShortplayMapResponse(server.URL + "/fallback"))
		case "/fallback":
			mu.Lock()
			fallbackCalls++
			mu.Unlock()
			_, _ = writer.Write([]byte(`{"Code":0,"video_info":{"Code":0,"data":{}}}`))
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	d := &Downloader{
		cfg: Config{
			dataDir: t.TempDir(), Retries: 1,
			HongguoRegisterURL:  server.URL + "/device_register",
			HongguoShortplayURL: server.URL + "/shortplay",
			HongguoPlayerURL:    server.URL + "/player_api",
			HongguoPlayerAltURL: server.URL + "/player_api_alt",
		},
		client: server.Client(), limiter: newRequestLimiter(3, 0), providerHosts: map[string]string{},
	}
	media, err := d.resolveHongguoPlayerMedia(context.Background(), hongguoPlayerTestVideo)
	if err != nil {
		t.Fatalf("map 形态播放模型取流失败: %v", err)
	}
	if got := hex.EncodeToString(media.CENCKey); got != "0f493590f2659b72732455a470f2caee" {
		t.Fatalf("CENCKey = %q，map 形态变体丢失了解密信息", got)
	}
	if media.Quality != 720 || len(media.Variants) != 3 {
		t.Fatalf("清晰度 = %d, 备选数 = %d, want 720/3", media.Quality, len(media.Variants))
	}
	mu.Lock()
	defer mu.Unlock()
	if fallbackCalls != 0 {
		t.Fatalf("内联变体存在时不应请求 fallback_api，实际 %d 次", fallbackCalls)
	}
}

func TestHongguoPlayerFallbackAddressShapes(t *testing.T) {
	const address = "https://vas-lf-x.snssdk.com/video/fplay/1/abc?aid=8662"
	encoded, _ := json.Marshal(map[string]any{"fallback_api": address})
	cases := []struct {
		name  string
		value any
		want  string
	}{
		{"纯字符串", address, address},
		{"对象", map[string]any{"fallback_api": address}, address},
		{"数组", []any{address, "https://else.example.com/x"}, address},
		{"JSON 字符串", string(encoded), address},
		{"无地址", map[string]any{}, ""},
		{"非 HTTP", "javascript:alert(1)", ""},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			if got := hongguoPlayerFallbackAddress(item.value); got != item.want {
				t.Fatalf("地址 = %q, want %q", got, item.want)
			}
		})
	}
}

func TestHongguoPlayerRejectsEncryptedRenditionWithoutKey(t *testing.T) {
	info := map[string]any{
		"video_list": []any{map[string]any{
			"definition": "720p", "vwidth": 720, "vheight": 1280, "codec_type": "h264",
			"main_url":     hongguoPlayerTestMedia,
			"encrypt_info": map[string]any{"encrypt": true, "encryption_method": "cenc-aes-ctr"},
		}},
	}
	if _, err := selectHongguoPlayerMedia(info, info); err == nil {
		t.Fatal("加密片源缺少 spade_a 时不应返回无密钥媒体")
	}
}

// 首条线路返回明文片源时，继续寻找带 CENC 密钥的线路，避免直接返回不可播媒体。
func TestHongguoPlayerPrefersKeyedRoute(t *testing.T) {
	keyless, _ := json.Marshal(map[string]any{
		"code": 0,
		"data": map[string]any{"video_model": mustJSON(map[string]any{
			"video_list": []any{map[string]any{
				"definition": "720p", "vwidth": 720, "vheight": 1280,
				"codec_type": "h264", "main_url": hongguoPlayerTestMedia,
			}},
		})},
	})
	var mu sync.Mutex
	var primaryCalls int
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/device_register":
			_, _ = writer.Write([]byte(hongguoPlayerTestRegisterResponse))
		case "/shortplay":
			_, _ = writer.Write(keyless)
		case "/player_api":
			mu.Lock()
			primaryCalls++
			mu.Unlock()
			_, _ = writer.Write(hongguoPlayerTestResponse())
		case "/player_api_alt":
			_, _ = writer.Write(hongguoPlayerTestResponse())
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	d := &Downloader{
		cfg: Config{
			dataDir: t.TempDir(), Retries: 1,
			HongguoRegisterURL:  server.URL + "/device_register",
			HongguoShortplayURL: server.URL + "/shortplay",
			HongguoPlayerURL:    server.URL + "/player_api",
			HongguoPlayerAltURL: server.URL + "/player_api_alt",
		},
		client: server.Client(), limiter: newRequestLimiter(3, 0), providerHosts: map[string]string{},
	}
	media, err := d.resolveHongguoPlayerMedia(context.Background(), hongguoPlayerTestVideo)
	if err != nil {
		t.Fatalf("应回退到带密钥的线路: %v", err)
	}
	if got := hex.EncodeToString(media.CENCKey); got != "0f493590f2659b72732455a470f2caee" {
		t.Fatalf("未选到带密钥的片源: %q", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if primaryCalls != 1 {
		t.Fatalf("主线路调用 %d 次, want 1", primaryCalls)
	}
}

func TestHongguoPlayerRefreshesDeviceOnEmptyResponses(t *testing.T) {
	original := hongguoPlayerRetryDelays
	hongguoPlayerRetryDelays = []time.Duration{0, 0, 0}
	defer func() { hongguoPlayerRetryDelays = original }()
	var mu sync.Mutex
	registerCalls, playCalls := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		mu.Lock()
		current := registerCalls
		mu.Unlock()
		switch request.URL.Path {
		case "/device_register":
			mu.Lock()
			registerCalls++
			mu.Unlock()
			_, _ = writer.Write([]byte(hongguoPlayerTestRegisterResponse))
		case "/shortplay", "/player_api", "/player_api_alt":
			mu.Lock()
			playCalls++
			mu.Unlock()
			if current == 1 {
				return
			}
			_, _ = writer.Write(hongguoPlayerTestResponse())
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	d := &Downloader{
		cfg: Config{
			dataDir: t.TempDir(), Retries: 1,
			HongguoRegisterURL:  server.URL + "/device_register",
			HongguoShortplayURL: server.URL + "/shortplay",
			HongguoPlayerURL:    server.URL + "/player_api",
			HongguoPlayerAltURL: server.URL + "/player_api_alt",
		},
		client: server.Client(), limiter: newRequestLimiter(3, 0), providerHosts: map[string]string{},
	}
	media, err := d.resolveHongguoPlayerMedia(context.Background(), hongguoPlayerTestVideo)
	if err != nil {
		t.Fatalf("空响应后未通过设备刷新恢复: %v", err)
	}
	if len(media.CENCKey) != 16 {
		t.Fatalf("恢复后片源缺少密钥: %d", len(media.CENCKey))
	}
	mu.Lock()
	defer mu.Unlock()
	if registerCalls != 2 || playCalls != 7 {
		t.Fatalf("注册 %d 次, 取流 %d 次, want 2/7", registerCalls, playCalls)
	}
}

func mustJSON(value any) string {
	raw, _ := json.Marshal(value)
	return string(raw)
}

func TestHongguoPlayerMediaURLVariants(t *testing.T) {
	seed, err := decodeHongguoBase64(hongguoPlayerTestSeed)
	if err != nil {
		t.Fatalf("seed 解码失败: %v", err)
	}
	cases := []struct {
		name      string
		candidate string
		want      string
		wantErr   bool
	}{
		{"明文地址", hongguoPlayerTestMedia, hongguoPlayerTestMedia, false},
		{"base64 明文地址", base64.StdEncoding.EncodeToString([]byte(hongguoPlayerTestMedia)), hongguoPlayerTestMedia, false},
		{"Spade 加密地址", hongguoPlayerTestSpadeURL, "https://v26-hgweb.qznovelvod.com/video/tos/spade.mp4?a=8662", false},
		{"不可信域名", "https://cdn.example.com/a.mp4", "", true},
		{"HTTP 明文地址", "http://v26-hgweb.qznovelvod.com/a.mp4", "", true},
		{"无效编码", "not-base64!!", "", true},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			got, err := hongguoPlayerMediaURL(item.candidate, seed)
			if item.wantErr {
				if err == nil {
					t.Fatalf("非法地址被接受: %q", got)
				}
				return
			}
			if err != nil || got != item.want {
				t.Fatalf("地址 = %q, err = %v, want %q", got, err, item.want)
			}
		})
	}
}
