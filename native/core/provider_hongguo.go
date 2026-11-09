package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

const hongguoBaseURL = "https://hongguoduanju.com"

func (d *Downloader) fetchHongguoDramas(ctx context.Context) ([]Drama, error) {
	initialized := hongguoCatalogInitialized(d.hongguoCatalogSnapshot())
	dramas, appErr := d.fetchHongguoAppCatalog(ctx)
	if len(dramas) > 0 || appErr == nil && initialized {
		return dramas, appErr
	}
	if ctx.Err() != nil {
		return dramas, ctx.Err()
	}
	webDramas, webErr := d.fetchHongguoWebDramas(ctx)
	if len(webDramas) > 0 && webErr == nil {
		return webDramas, nil
	}
	return webDramas, errors.Join(appErr, webErr)
}

func (d *Downloader) fetchHongguoWebDramas(ctx context.Context) ([]Drama, error) {
	routes := []struct {
		path string
		name string
	}{
		{path: "real-drama", name: "真人剧"},
		{path: "comic-drama", name: "漫剧"},
		{path: "ai-drama", name: "AI剧"},
		{path: "comic", name: "动漫"},
	}
	type routeResult struct {
		dramas []Drama
		err    error
	}
	results := make(chan routeResult, len(routes))
	for _, route := range routes {
		route := route
		go func() {
			dramas, err := d.fetchHongguoRoute(ctx, route.path, route.name)
			results <- routeResult{dramas: dramas, err: err}
		}()
	}
	seen := map[string]bool{}
	var out []Drama
	var failures []error
	for range routes {
		result := <-results
		if result.err != nil {
			failures = append(failures, result.err)
		}
		for _, drama := range result.dramas {
			if drama.ID != "" && !seen[drama.ID] {
				seen[drama.ID] = true
				out = append(out, drama)
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].DisplayTitle() < out[j].DisplayTitle() })
	return out, errors.Join(failures...)
}

func (d *Downloader) fetchHongguoRoute(ctx context.Context, route, category string) ([]Drama, error) {
	pages := d.cfg.MaxPagesPerSort
	if pages < 1 {
		pages = 1
	}
	var dramas []Drama
	seen := map[string]bool{}
	for page := 1; page <= pages; page++ {
		items, totalPages, err := d.fetchHongguoCategoryPage(ctx, route+"?page="+strconv.Itoa(page), category)
		if err != nil {
			return dramas, fmt.Errorf("%s: %w", category, err)
		}
		if totalPages > pages {
			pages = totalPages
			if pages > 500 {
				pages = 500
			}
		}
		reportLibraryProgress(ctx, sourceHongguo, items, nil, false)
		added := 0
		for _, drama := range items {
			if !seen[drama.ID] {
				seen[drama.ID] = true
				dramas = append(dramas, drama)
				added++
			}
		}
		if added == 0 || len(items) < 24 {
			break
		}
	}
	return dramas, nil
}

func (d *Downloader) fetchHongguoCategoryPage(ctx context.Context, route, category string) ([]Drama, int, error) {
	body, err := d.fetchProviderText(ctx, hongguoBaseURL+"/category/"+route, hongguoBaseURL+"/")
	if err != nil {
		return nil, 0, err
	}
	data := parseRouterData(body)
	page := routerLoaderMap(data, "category_page", "category_$")
	if len(page) == 0 || page["isSuccess"] == false {
		return nil, 0, errors.New("红果分类数据不可用，可能是页面结构或访问权限变化")
	}
	items := anyList(page["recommendList"])
	out := make([]Drama, 0, len(items))
	for _, item := range items {
		if dr := hongguoDramaFromAny(item, category); dr.ID != "" {
			out = append(out, dr)
		}
	}
	pages, _ := strconv.Atoi(mapString(nestedMap(page, "pagination"), "totalPages"))
	return out, pages, nil
}

func (d *Downloader) fetchHongguoChapters(ctx context.Context, sourceID string) (string, []Chapter, error) {
	sourceID = strings.TrimPrefix(strings.TrimSpace(sourceID), "hg-series-v1:")
	if sourceID == "" {
		return "", nil, fmt.Errorf("empty hongguo sourceID")
	}
	entry, appErr := d.hongguoAppDetail(ctx, sourceID)
	if appErr == nil {
		return entry.Drama.DisplayTitle(), append([]Chapter(nil), entry.Chapters...), nil
	}
	if ctx.Err() != nil {
		return "", nil, ctx.Err()
	}
	title, chapters, webErr := d.fetchHongguoWebChapters(ctx, sourceID)
	if webErr == nil {
		return title, chapters, nil
	}
	return "", nil, fmt.Errorf("App 详情失败: %v；网页详情失败: %w", publicError(appErr), webErr)
}

func (d *Downloader) fetchHongguoWebChapters(ctx context.Context, sourceID string) (string, []Chapter, error) {
	body, err := d.fetchProviderText(ctx, hongguoBaseURL+"/detail?series_id="+url.QueryEscape(sourceID), hongguoBaseURL+"/")
	if err != nil {
		return "", nil, err
	}
	data := parseRouterData(body)
	page := routerLoaderMap(data, "detail_page", "detail_")
	detail, _ := page["seriesDetail"].(map[string]any)
	if len(detail) == 0 {
		return "", nil, fmt.Errorf("hongguo detail is empty")
	}
	title := firstNonEmpty(mapString(detail, "series_name", "series_title", "name"), sourceID)
	vids := anyList(detail["vid_list"])
	// 站点在详情页明确声明网页端可公开播放的集数；实测登录账号也不改变这个边界。
	// 把这个范围带到选集界面，避免用户对锁定集反复发起注定失败的下载。
	accessible := hongguoAccessibleEpisodes(map[string]any{"seriesDetail": detail})
	chapters := make([]Chapter, 0, len(vids))
	for i, v := range vids {
		vid := strings.TrimSpace(fmt.Sprint(v))
		if vid == "" || vid == "<nil>" {
			continue
		}
		idx := i + 1
		chapters = append(chapters, Chapter{ID: providerChapterID(sourceHongguo, sourceID, vid), Source: sourceHongguo, Title: fmt.Sprintf("第%d集", idx), VideoURL: "hongguo-cenc://" + vid, CurrentEpisode: rawEpisode(idx), Locked: accessible > 0 && idx > accessible})
	}
	if len(chapters) == 0 {
		return title, nil, errors.New("红果详情没有返回剧集 ID")
	}
	return title, chapters, nil
}

func parseRouterData(raw string) map[string]any {
	idx := regexp.MustCompile(`(?s)(?:window\.)?_ROUTER_DATA\s*=\s*`).FindStringIndex(raw)
	if idx == nil {
		return nil
	}
	var data map[string]any
	dec := json.NewDecoder(strings.NewReader(raw[idx[1]:]))
	dec.UseNumber()
	if err := dec.Decode(&data); err != nil {
		return nil
	}
	return data
}

func (d *Downloader) resolveHongguoMedia(ctx context.Context, task Task) (providerMedia, error) {
	source, seriesID, ok := splitProviderDramaID(task.DramaID)
	if !ok || source != sourceHongguo {
		return providerMedia{}, fmt.Errorf("红果章节缺少剧 ID，请刷新剧库后重新加入队列")
	}
	videoID := strings.TrimPrefix(task.Chapter.VideoURL, "hongguo-cenc://")
	if !hongguoNumericID.MatchString(videoID) || !hongguoNumericID.MatchString(seriesID) {
		return providerMedia{}, fmt.Errorf("红果章节 ID 无效，请重新获取章节")
	}
	// 网页播放页最快，而且能唯一确定「该集是否被站点锁定」。放在最前面，
	// 公开集不必先等 App 网关（该网关目前对取流请求一律返回空 body）。
	media, pageErr := d.resolveHongguoWebMedia(ctx, seriesID, videoID)
	if pageErr == nil {
		return media, nil
	}
	var locked *hongguoEpisodeLockedError
	if errors.As(pageErr, &locked) {
		// 站点已 302 到试看集：授权结论确定，再试 App / 备用线路既无意义
		// 又会拖到超时，直接返回带公开集数的准确诊断。
		return providerMedia{}, locked
	}
	if err := ctx.Err(); err != nil {
		return providerMedia{}, err
	}
	// 播放页 SSR 会脱敏受保护分集的地址，改走网页播放器自己使用的公开回退接口。
	media, publicErr := d.resolveHongguoPublicFallback(ctx, seriesID, videoID)
	if publicErr == nil {
		return media, nil
	}
	if errors.Is(publicErr, errHongguoPublicEpisodeUnavailable) {
		// 403 public_fallback_unavailable 是站点对该集的明确授权拒绝，
		// 不是线路故障；不要再拼出四条混杂的失败原因误导用户。
		return providerMedia{}, fmt.Errorf("红果该集未在网页端开放公开播放；站点仅开放前若干集的网页直链，登录账号也不解除该限制，其余分集需在官方 App 观看；App 网关与备用线路无法绕过站点授权；不会用试看集冒充该集下载")
	}
	if err := ctx.Err(); err != nil {
		return providerMedia{}, err
	}
	// 只有网页与公开回退都因网络/解析失败时，才降级到 App 网关与备用线路。
	media, nativeErr := d.resolveHongguoAppMedia(ctx, videoID)
	if nativeErr == nil {
		return media, nil
	}
	if err := ctx.Err(); err != nil {
		return providerMedia{}, err
	}
	media, apiErr := d.resolveHongguoPlaybackAPI(ctx, seriesID, videoID)
	if apiErr == nil {
		return media, nil
	}
	return providerMedia{}, fmt.Errorf("红果网页取流失败：%v；公开回退取流失败：%v；App 取流失败：%v；备用取流失败：%w", publicError(pageErr), publicError(publicErr), publicError(nativeErr), apiErr)
}

func (d *Downloader) resolveHongguoWebMedia(ctx context.Context, seriesID, videoID string) (providerMedia, error) {
	pageURL := hongguoBaseURL + "/player/" + url.PathEscape(seriesID) + "/" + url.PathEscape(videoID)
	body, err := d.fetchProviderText(ctx, pageURL, hongguoBaseURL+"/")
	if err != nil {
		return providerMedia{}, err
	}
	page := routerLoaderMap(parseRouterData(body), "player_", "player_page")
	if mapString(page, "vid") != videoID || mapString(page, "series_id") != seriesID {
		// 锁定集会被 302 到试看集并在 URL 上带 pc_locked_episode=<原 vid>。
		// 跟随重定向后 page.vid 会变成试看集；此处必须拦住，绝不能把试看集
		// 的地址当成用户请求的那一集返回。同时把站点声明的游客可看集数带出，
		// 让上层直接短路，避免再空跑 App 网关与已停用的备用线路。
		if strings.Contains(body, "pc_locked_episode") || hongguoAccessibleEpisodes(page) > 0 {
			return providerMedia{}, &hongguoEpisodeLockedError{accessible: hongguoAccessibleEpisodes(page)}
		}
		return providerMedia{}, fmt.Errorf("红果未返回所请求的剧集，可能仅允许网页试看；请在站点确认该集的访问权限")
	}
	info, _ := page["video_player_info"].(map[string]any)
	addresses := hongguoMediaAddresses(info)
	if len(addresses) == 0 {
		// 新版播放页会在服务端脱敏：受保护分集只下发 protectedPlayback=true
		// 与空 videoList。播放器实际另调公开回退接口取址，因此这里只说明页面
		// 不含地址，由上层继续尝试公开回退，不能据此判定整集不可播。
		if protected, _ := page["protectedPlayback"].(bool); protected {
			return providerMedia{}, errors.New("红果播放页已脱敏该集地址（受保护内容），需改由网页公开回退接口取流；不会将试看集冒充该集下载")
		}
		return providerMedia{}, errors.New("红果播放页未提供公开播放地址，需改由网页公开回退接口取流；不会将试看集冒充该集下载")
	}
	duration, _ := strconv.ParseFloat(mapString(info, "duration"), 64)
	media := providerMedia{URL: addresses[0], Referer: d.providerBaseURL(sourceHongguo) + "/", Duration: time.Duration(duration * float64(time.Second))}
	for _, address := range addresses {
		variant := media
		variant.URL, variant.Variants = address, nil
		media.Variants = append(media.Variants, variant)
	}
	return media, nil
}

// hongguoAccessibleEpisodes 读取播放页 seriesDetail 里站点自己声明的
// 「游客可看集数」；网页与公开回退接口对这个边界的判定是一致的。
func hongguoAccessibleEpisodes(page map[string]any) int {
	detail, _ := page["seriesDetail"].(map[string]any)
	count, err := strconv.Atoi(mapString(detail, "accessible_episode_cnt"))
	if err != nil || count < 0 || count > 10000 {
		return 0
	}
	return count
}

func hongguoDramaFromAny(v any, category string) Drama {
	m, ok := v.(map[string]any)
	if !ok {
		return Drama{}
	}
	vd, _ := m["video_data"].(map[string]any)
	if len(vd) == 0 {
		vd = m
	}
	sourceID := firstNonEmpty(mapString(vd, "series_id_str", "series_id"), mapString(m, "series_id_str", "series_id"), mapString(vd, "keyword"), mapString(m, "keyword"))
	if !hongguoNumericID.MatchString(sourceID) {
		return Drama{}
	}
	title := firstNonEmpty(mapString(vd, "series_title", "series_name", "title"), mapString(m, "series_name", "name"), sourceID)
	cover := firstNonEmpty(mapString(vd, "series_cover", "cover"), mapString(m, "series_cover"))
	intro := firstNonEmpty(mapString(vd, "series_intro", "video_desc"), mapString(m, "series_intro"))
	count := firstNonEmpty(mapString(vd, "episode_cnt"), mapString(m, "episode_cnt"))
	remark := firstNonEmpty(mapString(vd, "episode_right_text"), mapString(m, "episode_right_text"))
	releaseStatus := releaseStatusFromRemark(remark)
	if mapString(vd, "series_status") == "1" {
		releaseStatus = "finished"
	} else if mapString(vd, "series_status") == "0" {
		releaseStatus = "ongoing"
	}
	if remark == "" && count != "" {
		remark = "共" + count + "集"
	}
	tags := mapStringSlice(vd, "tags")
	for _, value := range anyList(vd["category_list"]) {
		item, _ := value.(map[string]any)
		if name := mapString(item, "name"); name != "" && !slices.Contains(tags, name) {
			tags = append(tags, name)
		}
	}
	var categories []struct {
		Name string `json:"name"`
	}
	if json.Unmarshal([]byte(mapString(vd, "category_schema")), &categories) == nil {
		for _, item := range categories {
			if item.Name != "" && !slices.Contains(tags, item.Name) {
				tags = append(tags, item.Name)
			}
		}
	}
	genre := mapString(vd, "category_name", "categoryName", "category")
	if genre == "" && len(tags) > 0 {
		genre = tags[0]
	}
	return Drama{ID: providerDramaID(sourceHongguo, sourceID), Source: sourceHongguo, SourceID: sourceID, Title: title, Name: title, Desc: intro, Intro: intro, Cover: cover, CoverURL: cover, CategoryName: firstNonEmpty(genre, category), ChannelName: "红果", Remark: remark, TotalEpisode: count, EpisodeCount: count, Tags: tags, ReleaseStatus: releaseStatus, Score: mapString(vd, "score"), Views: mapString(vd, "series_play_cnt", "play_cnt"), Heat: hongguoHeat(vd), OnlineDate: hongguoOnlineDate(vd, time.Now())}
}

func nestedMap(v any, keys ...string) map[string]any {
	cur, _ := v.(map[string]any)
	for _, key := range keys {
		if cur == nil {
			return nil
		}
		cur, _ = cur[key].(map[string]any)
	}
	return cur
}

func routerLoaderMap(data map[string]any, names ...string) map[string]any {
	loader, _ := data["loaderData"].(map[string]any)
	for _, name := range names {
		if page, _ := loader[name].(map[string]any); len(page) > 0 {
			return page
		}
	}
	for key, value := range loader {
		for _, name := range names {
			if strings.TrimSuffix(name, "$") != "" && strings.HasPrefix(key, strings.TrimSuffix(name, "$")) {
				if page, _ := value.(map[string]any); len(page) > 0 {
					return page
				}
			}
		}
	}
	return nil
}

func anyList(v any) []any {
	switch x := v.(type) {
	case []any:
		return x
	case map[string]any:
		for _, key := range []string{"list", "items", "data"} {
			if out := anyList(x[key]); len(out) > 0 {
				return out
			}
		}
	}
	return nil
}
