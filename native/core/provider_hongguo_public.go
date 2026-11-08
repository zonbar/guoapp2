package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// 红果播放页的服务端渲染会主动脱敏播放地址：受保护分集只下发
// protectedPlayback=true 与空 videoList。但网页播放器自己会通过这个 BFF 接口
// 获取「公开分集」的直链，站点只对允许公开播放的 item_id 返回地址，其余返回
// 403 public_fallback_unavailable。此前只解析 SSR 页面，会把它误判成完全不可播。
const hongguoPublicFallbackPath = "/api/video/protected/fallback-play-info"

var errHongguoPublicEpisodeUnavailable = errors.New("该集未在站点开放公开播放，需登录或 App 授权")

// resolveHongguoPublicFallback 严格用用户请求的 videoID 取址，不枚举、不回退到
// 其它分集，因此绝不会把免费试看集的地址冒充成目标分集。
func (d *Downloader) resolveHongguoPublicFallback(ctx context.Context, seriesID, videoID string) (providerMedia, error) {
	if !hongguoNumericID.MatchString(seriesID) || !hongguoNumericID.MatchString(videoID) {
		return providerMedia{}, errors.New("红果公开取流请求缺少有效的剧集 ID")
	}
	base := strings.TrimRight(d.providerBaseURL(sourceHongguo), "/")
	if base == "" {
		return providerMedia{}, errors.New("红果站点地址未配置")
	}
	payload, err := json.Marshal(map[string]string{"item_id": videoID})
	if err != nil {
		return providerMedia{}, err
	}
	pageURL := base + "/player/" + url.PathEscape(seriesID) + "/" + url.PathEscape(videoID)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, base+hongguoPublicFallbackPath, bytes.NewReader(payload))
	if err != nil {
		return providerMedia{}, err
	}
	request.Header.Set("User-Agent", duanjuUserAgent)
	request.Header.Set("Accept", "application/json, text/plain, */*")
	request.Header.Set("Accept-Language", "zh-CN,zh;q=0.9")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", base)
	request.Header.Set("Referer", pageURL)
	// 403 表示「该集未授权」，不是站点封锁；避免让同站目录、封面请求一起进入退避。
	ctx = context.WithValue(ctx, skipBackoffObserveKey{}, true)
	request = request.WithContext(ctx)
	response, err := d.doCatalogRequestWithTimeout(request, providerTimeout)
	if err != nil {
		return providerMedia{}, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 256*1024+1))
	if err != nil {
		return providerMedia{}, err
	}
	if len(body) > 256*1024 {
		return providerMedia{}, errors.New("红果公开取流响应过大")
	}
	if response.StatusCode == http.StatusForbidden || response.StatusCode == http.StatusUnauthorized {
		return providerMedia{}, errHongguoPublicEpisodeUnavailable
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return providerMedia{}, fmt.Errorf("红果公开取流接口 HTTP %d", response.StatusCode)
	}
	return parseHongguoPublicFallback(body, base)
}

func parseHongguoPublicFallback(body []byte, base string) (providerMedia, error) {
	var decoded map[string]any
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := decoder.Decode(&decoded); err != nil || decoded == nil {
		return providerMedia{}, errors.New("红果公开取流接口返回了无效数据")
	}
	if reason := mapString(decoded, "error"); strings.Contains(reason, "unavailable") {
		return providerMedia{}, errHongguoPublicEpisodeUnavailable
	}
	if code := mapString(nestedMap(decoded, "base_resp"), "StatusCode"); code != "" && code != "0" {
		return providerMedia{}, fmt.Errorf("红果公开取流接口暂不可用（%s）", truncate(code, 20))
	}
	address := strings.TrimSpace(mapString(decoded, "main_url"))
	if len(address) > 8192 || !isProviderHTTPMediaURL(address) {
		return providerMedia{}, errHongguoPublicEpisodeUnavailable
	}
	return providerMedia{URL: address, Referer: base + "/"}, nil
}
