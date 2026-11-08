package core

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

type nativeHealthStep struct {
	Name       string `json:"name"`
	State      string `json:"state"`
	Message    string `json:"message"`
	Host       string `json:"host,omitempty"`
	HTTPStatus int    `json:"httpStatus,omitempty"`
	ElapsedMS  int64  `json:"elapsedMs"`
	CFRay      string `json:"cfRay,omitempty"`
}

type nativeSourceHealth struct {
	CheckedAt time.Time          `json:"checkedAt"`
	State     string             `json:"state"`
	Sample    string             `json:"sample"`
	Steps     []nativeHealthStep `json:"steps"`
}

type sourceResponseTrace struct {
	mu   sync.Mutex
	last nativeHealthStep
}

type sourceTraceKey struct{}

func observeSourceResponse(ctx context.Context, response *http.Response) {
	trace, _ := ctx.Value(sourceTraceKey{}).(*sourceResponseTrace)
	if trace == nil || response == nil {
		return
	}
	step := nativeHealthStep{HTTPStatus: response.StatusCode, CFRay: truncate(response.Header.Get("Cf-Ray"), 128)}
	if response.Request != nil && response.Request.URL != nil {
		step.Host = response.Request.URL.Hostname()
	}
	trace.mu.Lock()
	trace.last = step
	trace.mu.Unlock()
}

func (engine *nativeEngine) checkSource(ctx context.Context, source string, selected nativeDrama, playback bool) error {
	report := nativeSourceHealth{CheckedAt: time.Now(), State: "checking", Steps: []nativeHealthStep{}}
	trace := &sourceResponseTrace{}
	ctx = context.WithValue(ctx, sourceTraceKey{}, trace)
	// 用户主动发起的检测必须真实发出请求，不能被上一次失败留下的退避直接挡回。
	ctx = context.WithValue(ctx, forceRefreshKey{}, true)
	publish := func() {
		copy := report
		copy.Steps = append([]nativeHealthStep{}, report.Steps...)
		engine.changeSourceRecord(source, func(record *nativeSourceRecord) { record.Health = &copy })
	}
	var failure error
	defer func() {
		report.CheckedAt = time.Now()
		if failure != nil {
			report.State = "failed"
		} else if playback {
			report.State = "ok"
		} else {
			report.State = "catalogOnly"
		}
		publish()
	}()
	run := func(name string, action func() (string, error)) bool {
		engine.changeSourceRecord(source, func(record *nativeSourceRecord) {
			record.Stage, record.Completed = name, len(report.Steps)
			record.Total = 1
			if playback {
				record.Total = 5
			}
		})
		trace.mu.Lock()
		trace.last = nativeHealthStep{}
		trace.mu.Unlock()
		start := time.Now()
		message, err := action()
		trace.mu.Lock()
		step := trace.last
		trace.mu.Unlock()
		step.Name, step.State, step.Message, step.ElapsedMS = name, "ok", message, time.Since(start).Milliseconds()
		if err != nil {
			failure = err
			step.State, step.Message = "failed", publicError(err).Error()
			var backoff *requestBackoff
			if errors.As(err, &backoff) {
				step.HTTPStatus, step.Host = backoff.status, backoff.host
			}
		}
		report.Steps = append(report.Steps, step)
		publish()
		return err == nil
	}
	var page nativeCatalogResult
	if !run("入口与目录", func() (string, error) {
		var err error
		page, err = engine.nativeCatalog(ctx, nativeInput{Source: source, Page: 1, Force: true})
		if err == nil && page.Warning != "" {
			err = errors.New(page.Warning)
		}
		if err == nil && len(page.Items) == 0 {
			err = errors.New("入口可达，但没有解析到有效剧集")
		}
		return fmt.Sprintf("已解析 %d 部剧", len(page.Items)), err
	}) || !playback {
		return failure
	}
	drama := selected
	if drama.ID == "" {
		drama = page.Items[0]
		for _, item := range page.Items {
			if item.VIP == nil || !*item.VIP {
				drama = item
				break
			}
		}
	}
	// 部分源的单部剧可能全部为付费分集（如帝果），只测第一部会把「选中的剧
	// 没有免费集」误判为整站不可用。检测时最多再试三部非 VIP 剧目。
	candidates := []nativeDrama{drama}
	for _, item := range page.Items {
		if item.ID == drama.ID || (item.VIP != nil && *item.VIP) {
			continue
		}
		candidates = append(candidates, item)
		if len(candidates) >= 4 {
			break
		}
	}
	report.Sample = drama.Title
	var chapter Chapter
	if !run("分集目录", func() (string, error) {
		var lastErr error
		for _, candidate := range candidates {
			_, id, _ := splitProviderDramaID(candidate.ID)
			_, chapters, err := engine.downloader.GetHuangguoChapters(ctx, source, id)
			if err != nil {
				lastErr = err
				continue
			}
			for _, item := range chapters {
				if !item.VIP {
					drama, chapter = candidate, item
					report.Sample = drama.Title
					return fmt.Sprintf("%d 集，检测第 %s 集", len(chapters), item.EpisodeString(1)), nil
				}
			}
			lastErr = errors.New("没有可用于检测的免费分集")
		}
		if lastErr != nil {
			return "", lastErr
		}
		return "", errors.New("没有可用于检测的免费分集")
	}) {
		return failure
	}
	var media providerMedia
	var keyURL, segmentURL string
	if !run("播放地址与播放列表", func() (string, error) {
		// 解析接口偶尔会挂死到分钟级，给这一步一个总预算，避免单次检测长时间卡住。
		probeCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
		defer cancel()
		var err error
		media, err = engine.downloader.resolveProviderMedia(probeCtx, Task{DramaID: drama.ID, Chapter: chapter, Index: 1})
		if err != nil {
			return "", err
		}
		ctx = providerMediaContext(ctx, media.credentials)
		keyURL, segmentURL, err = engine.downloader.sourceProbeResources(probeCtx, media)
		return "已取得可识别的播放地址", err
	}) {
		return failure
	}
	if !run("播放密钥", func() (string, error) {
		if len(media.CENCKey) == 16 || len(media.HLSKey) == 16 {
			return "密钥长度有效", nil
		}
		if keyURL == "" {
			return "当前媒体无需额外密钥", nil
		}
		if strings.HasPrefix(keyURL, "data:") {
			_, encoded, found := strings.Cut(keyURL, ",")
			decoded, err := base64.StdEncoding.DecodeString(encoded)
			if !found || err != nil || len(decoded) != 16 {
				return "", errors.New("内嵌密钥格式无效")
			}
			return "内嵌密钥有效", nil
		}
		body, err := engine.downloader.sourceProbeBytes(ctx, keyURL, media.Referer, true)
		if err == nil && len(body) != 16 {
			err = errors.New("返回的播放密钥不是 16 字节")
		}
		return "密钥可达且长度有效", err
	}) {
		return failure
	}
	run("媒体连接", func() (string, error) {
		body, err := engine.downloader.sourceProbeBytes(ctx, segmentURL, media.Referer, false)
		if err == nil && len(body) == 0 {
			err = errors.New("媒体服务器返回空内容")
		}
		return "已读取媒体开头；实际播放效果请在播放器确认", err
	})
	engine.changeSourceRecord(source, func(record *nativeSourceRecord) { record.Completed = len(report.Steps) })
	return failure
}

func (d *Downloader) sourceProbeResources(ctx context.Context, media providerMedia) (string, string, error) {
	ctx = providerMediaContext(ctx, media.credentials)
	if media.Playlist == "" {
		return "", media.URL, nil
	}
	body, address := media.Playlist, media.URL
	for depth := 0; depth < 6; depth++ {
		lines, _, master := nativeHLSSelect(strings.Split(body, "\n"), 480)
		base, err := url.Parse(address)
		if err != nil {
			return "", "", err
		}
		resolve := func(raw string) string {
			ref, err := url.Parse(strings.TrimSpace(raw))
			if err != nil {
				return ""
			}
			return base.ResolveReference(ref).String()
		}
		key, segment := "", ""
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "#EXT-X-KEY:") {
				attributes := nativeHLSAttributes(line)
				if attributes["METHOD"] == "AES-128" {
					key = resolve(attributes["URI"])
				} else if attributes["METHOD"] != "NONE" {
					return "", "", errors.New("当前密钥格式不支持健康检测")
				}
			}
			if line != "" && !strings.HasPrefix(line, "#") {
				segment = resolve(line)
				break
			}
		}
		if !isProviderHTTPMediaURL(segment) {
			return "", "", errors.New("播放列表缺少有效媒体地址")
		}
		if !master {
			return key, segment, nil
		}
		body, address, err = d.fetchMediaPlaylist(ctx, segment, media.Referer)
		if err != nil {
			return "", "", err
		}
	}
	return "", "", errors.New("播放列表嵌套过多")
}

func (d *Downloader) sourceProbeBytes(ctx context.Context, address, referer string, key bool) ([]byte, error) {
	if !isProviderHTTPMediaURL(address) {
		return nil, errors.New("媒体地址无效")
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Referer", referer)
	limit := int64(1024)
	if key {
		limit = 17
	} else {
		request.Header.Set("Range", "bytes=0-1023")
	}
	response, err := d.doMediaRequest(request)
	if err != nil && isTransientNetworkError(err) && ctx.Err() == nil {
		// CDN 连接重置 / EOF 常为瞬时抖动，在剩余预算内重放一次；
		// 重试失败时保留原始错误，避免把超时覆盖成无意义的 context canceled。
		if retryResponse, retryErr := d.doMediaRequest(request.Clone(ctx)); retryErr == nil {
			response, err = retryResponse, nil
		} else if retryResponse != nil && retryResponse.Body != nil {
			retryResponse.Body.Close()
		}
	}
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusPartialContent {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 64<<10))
		return nil, d.catalogResponseError(request, response, body)
	}
	contentType := strings.ToLower(response.Header.Get("Content-Type"))
	if strings.Contains(contentType, "text/html") || strings.Contains(contentType, "application/json") {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 64<<10))
		if catalogResponseBlockReason(response, body) != "" {
			return nil, d.catalogResponseError(request, response, body)
		}
		return nil, errors.New("媒体接口返回了网页或错误信息")
	}
	return io.ReadAll(io.LimitReader(response.Body, limit))
}
