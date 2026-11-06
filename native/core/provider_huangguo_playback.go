package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// 黄果 AI 新版播放页不再把播放地址写进 HTML：页面脚本会在首播或切集时
// POST /api/videos/{id}/episodes/{ep}/playback 换取一次性签名清单，
// 并随清单请求附带站点下发的 CDN 鉴权头。旧版 HTML 直出地址仍然优先。
type huangguoAIPlayback struct {
	URL        string
	CDNHeaders map[string]string
}

type huangguoAIPlaybackEnvelope struct {
	Status int    `json:"status"`
	Msg    string `json:"msg"`
	Data   struct {
		ManifestMethod string `json:"manifest_method"`
		Streams        struct {
			Main string `json:"main"`
			HEVC string `json:"hevc"`
		} `json:"streams"`
		CDN struct {
			M3U8Enabled bool   `json:"m3u8Enabled"`
			AuthKey     string `json:"authKey"`
			AuthValue   string `json:"authValue"`
			App         string `json:"app"`
		} `json:"cdn"`
	} `json:"data"`
}

var (
	reHuangguoAIEpisodePath = regexp.MustCompile(`/ep-(\d+)(?:/|$)`)
	reHTTPHeaderName        = regexp.MustCompile(`^[A-Za-z0-9-]+$`)
)

func parseHuangguoAIPlayback(payload []byte, pageURL string) (huangguoAIPlayback, error) {
	var envelope huangguoAIPlaybackEnvelope
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return huangguoAIPlayback{}, errors.New("黄果 AI 播放接口返回的数据无法识别")
	}
	if envelope.Status != 1 {
		if message := strings.TrimSpace(envelope.Msg); message != "" {
			return huangguoAIPlayback{}, fmt.Errorf("黄果 AI 播放接口拒绝取址：%s", message)
		}
		return huangguoAIPlayback{}, errors.New("黄果 AI 播放接口未返回成功状态")
	}
	if method := strings.ToUpper(strings.TrimSpace(envelope.Data.ManifestMethod)); method != "GET" {
		return huangguoAIPlayback{}, fmt.Errorf("黄果 AI 播放接口返回了不支持的取流方式 %q", method)
	}
	address := normalizeProviderMediaURL(pageURL, envelope.Data.Streams.Main)
	if address == "" {
		return huangguoAIPlayback{}, errors.New("黄果 AI 播放接口未返回有效的播放清单")
	}
	result := huangguoAIPlayback{URL: address}
	cdn := envelope.Data.CDN
	if cdn.M3U8Enabled && cdn.AuthValue != "" && cdn.App != "" && reHTTPHeaderName.MatchString(cdn.AuthKey) {
		result.CDNHeaders = map[string]string{
			http.CanonicalHeaderKey(cdn.AuthKey): cdn.AuthValue,
			"X-Cdn-App":                          cdn.App,
		}
	}
	return result, nil
}

func huangguoAIVideoID(pageURL string) string {
	parsed, err := url.Parse(pageURL)
	if err != nil {
		return ""
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	var candidate string
	for index, part := range parts {
		if (part == "video" || part == "detail") && index+1 < len(parts) {
			candidate = cleanID(parts[index+1])
			break
		}
	}
	if number, err := strconv.ParseUint(candidate, 10, 64); err != nil || number == 0 {
		return ""
	}
	return candidate
}

func huangguoAIEpisodeNumber(pageURL string, chapter Chapter, fallback int) int {
	if match := reHuangguoAIEpisodePath.FindStringSubmatch(pageURL); len(match) > 1 {
		if number, err := strconv.Atoi(match[1]); err == nil && number > 0 {
			return number
		}
	}
	if number := chapterEpisodeNumber(chapter, 0); number > 0 {
		return number
	}
	if fallback > 0 {
		return fallback
	}
	return 1
}

func (d *Downloader) fetchHuangguoAIPlayback(ctx context.Context, pageURL, sourceID string, ep int) (huangguoAIPlayback, error) {
	parsed, err := url.Parse(pageURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return huangguoAIPlayback{}, errors.New("黄果 AI 播放页地址无效")
	}
	videoID := strings.TrimSpace(sourceID)
	if number, err := strconv.ParseUint(videoID, 10, 64); err != nil || number == 0 {
		videoID = huangguoAIVideoID(pageURL)
	}
	if videoID == "" {
		return huangguoAIPlayback{}, errors.New("无法识别黄果 AI 剧集编号")
	}
	if ep < 1 {
		ep = 1
	}
	origin := parsed.Scheme + "://" + parsed.Host
	address := fmt.Sprintf("%s/api/videos/%s/episodes/%d/playback", origin, url.PathEscape(videoID), ep)
	payload, err := d.duanjuDo(ctx, duanjuRequest{
		Method:  http.MethodPost,
		Address: address,
		Referer: pageURL,
		Agent:   userAgent,
		Headers: map[string]string{
			"Accept":       "application/json, text/plain, */*",
			"Content-Type": "application/json",
			"Origin":       origin,
		},
		Body: []byte("{}"),
	})
	if err != nil {
		return huangguoAIPlayback{}, fmt.Errorf("黄果 AI 播放取址失败：%w", err)
	}
	return parseHuangguoAIPlayback(payload, pageURL)
}
