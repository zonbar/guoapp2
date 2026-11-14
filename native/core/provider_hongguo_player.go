package core

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/bits"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

const hongguoPlayerAPI = "https://reading.snssdk.com/novel/player/multi_video_model/player/v1"
const hongguoPlayerAltAPI = "https://api5-normal-sinfonlinea.fqnovel.com/novel/player/multi_video_model/player/v1"
const hongguoShortplayAPI = "https://api3-normal-sinfonlinea.fqnovel.com/novel/player/video_model/v1/"

var hongguoPlayerMediaHosts = []string{"qznovelvod.com", "fqnovelvod.com", "douyinvod.com"}

var errHongguoPlayerEmptyResponse = errors.New("接口返回空响应")

var hongguoPlayerRetryDelays = []time.Duration{0, 500 * time.Millisecond, 1200 * time.Millisecond}

var hongguoPlayerCleaner = strings.NewReplacer(`\/`, `/`, `\u0026`, `&`, `&amp;`, `&`)

type hongguoPlayerRoute struct {
	name      string
	endpoint  string
	shortplay bool
}

func (d *Downloader) hongguoPlayerRoutes() []hongguoPlayerRoute {
	return []hongguoPlayerRoute{
		{name: "番茄通道", endpoint: firstNonEmpty(d.cfg.HongguoShortplayURL, hongguoShortplayAPI), shortplay: true},
		{name: "App 播放", endpoint: firstNonEmpty(d.cfg.HongguoPlayerURL, hongguoPlayerAPI)},
		{name: "App 备用", endpoint: firstNonEmpty(d.cfg.HongguoPlayerAltURL, hongguoPlayerAltAPI)},
	}
}

type hongguoPlayerRequest struct {
	url     string
	body    []byte
	headers map[string]string
}

func hongguoPlaybackQuery(device *hongguoPlaybackDevice, now int64) [][2]string {
	return [][2]string{
		{"ac", "wifi"}, {"channel", "update_64"}, {"aid", "8662"}, {"app_name", "novelread"},
		{"version_code", "74332"}, {"version_name", "7.4.3.32"}, {"device_platform", "android"},
		{"os", "android"}, {"ssmix", "a"}, {"device_type", "25053RT47C"}, {"device_brand", "Redmi"},
		{"language", "zh"}, {"os_api", "36"}, {"os_version", "16"}, {"manifest_version_code", "74332"},
		{"resolution", "1280*2772"}, {"dpi", "520"}, {"update_version_code", "74332"},
		{"host_abi", "arm64-v8a"}, {"dragon_device_type", "phone"}, {"pv_player", "74332"},
		{"compliance_status", "0"}, {"need_personal_recommend", "1"}, {"player_so_load", "1"},
		{"is_android_pad_screen", "0"},
		{"device_id", device.DeviceID}, {"iid", device.InstallID}, {"cdid", device.CDID},
		{"_rticket", strconv.FormatInt(now, 10)},
	}
}

func hongguoShortplayQuery(device *hongguoPlaybackDevice) [][2]string {
	return [][2]string{
		{"iid", device.InstallID}, {"device_id", device.DeviceID}, {"aid", "1967"},
		{"app_name", "novelapp"}, {"version_code", "72132"}, {"version_name", "7.2.1.32"},
		{"device_platform", "android"}, {"device_brand", "Xiaomi"}, {"os_version", "13"},
		{"cdid", device.CDID},
	}
}

func hongguoPlayerRequestFor(route hongguoPlayerRoute, videoID string, device *hongguoPlaybackDevice) (*hongguoPlayerRequest, error) {
	now := time.Now().UnixMilli()
	var values [][2]string
	var payload map[string]any
	var body []byte
	headers := map[string]string{}
	if route.shortplay {
		values = hongguoShortplayQuery(device)
		payload = map[string]any{
			"video_id": videoID, "content_type": 1004,
			"biz_param": map[string]any{
				"detail_page_version": 0, "device_level": 3, "disable_digg_stat": false,
				"disable_video_relate_book": false, "from_video_id": "", "need_all_video_definition": true,
				"need_mp4_align": false, "source": 4, "use_os_player": false, "use_server_dns": false,
				"video_platform": 3,
			},
		}
		raw, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		body = raw
		headers["User-Agent"] = "com.dragon.read"
		headers["Content-Type"] = "application/json; charset=utf-8"
	} else {
		values = hongguoPlaybackQuery(device, now)
		payload = map[string]any{
			"dr_scene": "player",
			"biz_param": map[string]any{
				"detail_page_version": 0, "device_level": 3, "disable_digg_stat": false,
				"need_all_video_definition": true, "need_mp4_align": false, "use_os_player": false,
				"use_server_dns": false, "video_platform": 1024,
			},
			"mixed_video_id_map": map[string]any{"1004": []string{videoID}},
		}
		raw, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		var compressed bytes.Buffer
		writer := gzip.NewWriter(&compressed)
		if _, err := writer.Write(raw); err != nil {
			return nil, err
		}
		if err := writer.Close(); err != nil {
			return nil, err
		}
		body = compressed.Bytes()
		headers["User-Agent"] = hongguoPlaybackUserAgent
		headers["Accept"] = "application/json; charset=utf-8,application/x-protobuf"
		headers["Content-Type"] = "application/json; charset=UTF-8"
		headers["Content-Encoding"] = "gzip"
		headers["Sdk-Version"] = "2"
		headers["Passport-Sdk-Version"] = "5051452"
		headers["X-XS-From-Web"] = "0"
		headers["X-SS-DP"] = "8662"
	}
	query := hongguoPlayerEncodeQuery(values)
	if device.Cookie != "" {
		headers["Cookie"] = device.Cookie
	}
	for key, value := range hongguoPlayerSignHeaders(query, body, uint32(time.Now().Unix())) {
		headers[key] = value
	}
	return &hongguoPlayerRequest{url: route.endpoint + "?" + query, body: body, headers: headers}, nil
}

func (d *Downloader) fetchHongguoPlayer(ctx context.Context, request *hongguoPlayerRequest) (map[string]any, error) {
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, request.url, bytes.NewReader(request.body))
	if err != nil {
		return nil, err
	}
	for key, value := range request.headers {
		httpRequest.Header.Set(key, value)
	}
	response, err := d.doCatalogRequestWithTimeout(httpRequest, 15*time.Second)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	content, err := io.ReadAll(io.LimitReader(response.Body, 2<<20))
	if err != nil {
		return nil, err
	}
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", response.StatusCode)
	}
	if len(bytes.TrimSpace(content)) == 0 {
		return nil, errHongguoPlayerEmptyResponse
	}
	var result map[string]any
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.UseNumber()
	if err := decoder.Decode(&result); err != nil || result == nil {
		return nil, errors.New("接口返回格式异常")
	}
	return result, nil
}

func (d *Downloader) resolveHongguoPlayerMedia(ctx context.Context, videoID string) (providerMedia, error) {
	if !hongguoNumericID.MatchString(videoID) {
		return providerMedia{}, errors.New("红果播放请求缺少有效的分集 ID")
	}
	var lastErr error
	for attempt, delay := range hongguoPlayerRetryDelays {
		if delay > 0 {
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return providerMedia{}, ctx.Err()
			case <-timer.C:
			}
		}
		if attempt == len(hongguoPlayerRetryDelays)-1 {
			d.refreshHongguoPlaybackDevice(ctx)
		}
		media, err, rejected := d.tryHongguoPlayerRoutes(ctx, videoID)
		if err == nil {
			return media, nil
		}
		lastErr = err
		if !rejected {
			break
		}
	}
	return providerMedia{}, lastErr
}

func (d *Downloader) tryHongguoPlayerRoutes(ctx context.Context, videoID string) (providerMedia, error, bool) {
	device := d.hongguoPlaybackDevice(ctx)
	routes := d.hongguoPlayerRoutes()
	var failures []string
	var keyless *providerMedia
	empty := 0
	for _, route := range routes {
		if err := ctx.Err(); err != nil {
			return providerMedia{}, err, false
		}
		request, err := hongguoPlayerRequestFor(route, videoID, device)
		if err != nil {
			failures = append(failures, route.name+": "+err.Error())
			continue
		}
		result, err := d.fetchHongguoPlayer(ctx, request)
		if err != nil {
			failures = append(failures, route.name+": "+err.Error())
			if errors.Is(err, errHongguoPlayerEmptyResponse) {
				empty++
			}
			continue
		}
		media, err := d.parseHongguoPlayerResponse(ctx, result, videoID)
		if err != nil {
			failures = append(failures, route.name+": "+err.Error())
			continue
		}
		if len(media.CENCKey) == aes.BlockSize {
			return media, nil, false
		}
		if keyless == nil {
			candidate := media
			keyless = &candidate
		}
		failures = append(failures, route.name+": 片源未返回解密密钥")
	}
	if keyless != nil {
		return *keyless, nil, false
	}
	return providerMedia{}, fmt.Errorf("红果官方播放线路取流失败：%s", strings.Join(failures, "；")), empty > 0 && empty == len(routes)
}

func (d *Downloader) parseHongguoPlayerResponse(ctx context.Context, result map[string]any, videoID string) (providerMedia, error) {
	code := firstNonEmpty(mapString(result, "code", "Code"), mapString(nestedMap(result, "BaseResp"), "StatusCode"), "0")
	if code != "0" {
		return providerMedia{}, fmt.Errorf("接口未放行（%s）", truncate(code, 20))
	}
	data := nestedMap(result, "data")
	if data == nil {
		data = result
	}
	entry := data
	if found, ok := data[videoID].(map[string]any); ok {
		entry = found
	}
	model, err := hongguoPlayerModel(entry)
	if err != nil {
		return providerMedia{}, err
	}
	info := model
	if len(hongguoPlayerVariants(model["video_list"])) == 0 {
		address := hongguoPlayerFallbackAddress(model["fallback_api"])
		if address == "" {
			address = hongguoPlayerFallbackAddress(entry["fallback_api"])
		}
		info, err = d.fetchHongguoPlayerFallback(ctx, address)
		if err != nil {
			return providerMedia{}, err
		}
	}
	return selectHongguoPlayerMedia(info, model)
}

func hongguoPlayerVariants(value any) []any {
	switch list := value.(type) {
	case []any:
		return list
	case map[string]any:
		keys := make([]string, 0, len(list))
		for key := range list {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		rows := make([]any, 0, len(keys))
		for _, key := range keys {
			rows = append(rows, list[key])
		}
		return rows
	case string:
		var parsed any
		decoder := json.NewDecoder(strings.NewReader(list))
		decoder.UseNumber()
		if err := decoder.Decode(&parsed); err == nil {
			return hongguoPlayerVariants(parsed)
		}
	}
	return nil
}

func hongguoPlayerFallbackAddress(value any) string {
	switch item := value.(type) {
	case string:
		text := strings.TrimSpace(item)
		if strings.HasPrefix(text, "{") {
			var parsed map[string]any
			if err := json.Unmarshal([]byte(text), &parsed); err == nil {
				return hongguoPlayerFallbackAddress(parsed["fallback_api"])
			}
		}
		if isProviderHTTPMediaURL(text) {
			return text
		}
	case map[string]any:
		return hongguoPlayerFallbackAddress(item["fallback_api"])
	case []any:
		for _, candidate := range item {
			if address := hongguoPlayerFallbackAddress(candidate); address != "" {
				return address
			}
		}
	}
	return ""
}

func hongguoPlayerModel(entry map[string]any) (map[string]any, error) {
	switch value := entry["video_model"].(type) {
	case map[string]any:
		return value, nil
	case string:
		var model map[string]any
		decoder := json.NewDecoder(strings.NewReader(value))
		decoder.UseNumber()
		if err := decoder.Decode(&model); err != nil || model == nil {
			return nil, errors.New("播放模型格式异常")
		}
		return model, nil
	default:
		return nil, errors.New("接口未返回播放模型")
	}
}

func (d *Downloader) fetchHongguoPlayerFallback(ctx context.Context, address string) (map[string]any, error) {
	if !isProviderHTTPMediaURL(address) {
		return nil, errors.New("接口未提供有效的播放信息地址")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("User-Agent", hongguoPlaybackUserAgent)
	response, err := d.doCatalogRequestWithTimeout(request, 12*time.Second)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	content, err := io.ReadAll(io.LimitReader(response.Body, 2<<20))
	if err != nil {
		return nil, err
	}
	var outer map[string]any
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.UseNumber()
	if err := decoder.Decode(&outer); err != nil {
		return nil, errors.New("播放信息地址返回格式异常")
	}
	videoInfo := nestedMap(outer, "video_info")
	if code := firstNonEmpty(mapString(outer, "Code", "code"), "0"); code != "0" {
		return nil, fmt.Errorf("播放信息未放行（%s）", truncate(code, 20))
	}
	if code := firstNonEmpty(mapString(videoInfo, "Code", "code"), "0"); code != "0" {
		return nil, fmt.Errorf("播放信息未放行（%s）", truncate(code, 20))
	}
	info := nestedMap(videoInfo, "data")
	if info == nil {
		return nil, errors.New("播放信息缺少视频模型")
	}
	return info, nil
}

type hongguoPlayerChoice struct {
	media     providerMedia
	pixels    int
	codecRank int
}

func selectHongguoPlayerMedia(info, model map[string]any) (providerMedia, error) {
	rows := hongguoPlayerVariants(info["video_list"])
	if len(rows) == 0 {
		return providerMedia{}, errors.New("播放模型未提供可用清晰度")
	}
	duration, _ := strconv.ParseFloat(firstNonEmpty(mapString(info, "video_duration"), mapString(model, "video_duration")), 64)
	keySeed := firstNonEmpty(mapString(info, "key_seed"), mapString(model, "key_seed"))
	var seed []byte
	if keySeed != "" {
		seed, _ = decodeHongguoBase64(keySeed)
	}
	var choices []hongguoPlayerChoice
	seen := map[string]bool{}
	var keyErr error
	for _, row := range rows {
		variant, _ := row.(map[string]any)
		if len(variant) == 0 {
			continue
		}
		meta := nestedMap(variant, "video_meta")
		codec := strings.ToLower(firstNonEmpty(mapString(variant, "codec_type"), mapString(meta, "codec_type")))
		if codec == "bytevc2" || codec == "bvc2" {
			continue
		}
		rank := 3
		switch codec {
		case "h264", "avc", "avc1":
			rank = 0
		case "hevc", "h265", "hvc1", "hev1", "bytevc1":
			rank = 1
		}
		width, _ := strconv.Atoi(firstNonEmpty(mapString(variant, "vwidth"), mapString(meta, "vwidth")))
		height, _ := strconv.Atoi(firstNonEmpty(mapString(variant, "vheight"), mapString(meta, "vheight")))
		definition := firstNonEmpty(mapString(variant, "quality_desc", "definition"), mapString(meta, "definition"), mapString(variant, "height"))
		pixels := 0
		if width > 0 && height > 0 {
			if width < height {
				pixels = width
			} else {
				pixels = height
			}
		}
		if pixels == 0 {
			pixels, _ = strconv.Atoi(hongguoQualityNumber.FindString(definition))
		}
		encryptInfo := nestedMap(variant, "encrypt_info")
		spade := firstNonEmpty(mapString(variant, "spade_a"), mapString(encryptInfo, "spade_a"))
		if spade == "" && hongguoPlayerRenditionEncrypted(variant, encryptInfo) {
			keyErr = errors.New("片源标记为加密但缺少解密信息")
			continue
		}
		key, err := hongguoPlayerContentKey(spade)
		if err != nil {
			keyErr = err
			continue
		}
		for _, candidate := range hongguoPlayerCandidates(variant) {
			address, err := hongguoPlayerMediaURL(candidate, seed)
			if err != nil {
				continue
			}
			identity := address + ":" + hex.EncodeToString(key)
			if seen[identity] {
				continue
			}
			seen[identity] = true
			choices = append(choices, hongguoPlayerChoice{
				media: providerMedia{
					URL: address, Referer: "https://novel.snssdk.com/", CENCKey: key,
					Quality: pixels, Duration: time.Duration(duration * float64(time.Second)),
				},
				pixels: pixels, codecRank: rank,
			})
		}
	}
	if len(choices) == 0 {
		if keyErr != nil {
			return providerMedia{}, fmt.Errorf("红果片源密钥不可用: %w", keyErr)
		}
		return providerMedia{}, errors.New("播放模型未提供兼容的媒体地址")
	}
	sort.SliceStable(choices, func(i, j int) bool {
		if choices[i].pixels != choices[j].pixels {
			return choices[i].pixels > choices[j].pixels
		}
		return choices[i].codecRank < choices[j].codecRank
	})
	selected := choices[0].media
	for _, choice := range choices {
		selected.Variants = append(selected.Variants, choice.media)
	}
	return selected, nil
}

func hongguoPlayerCandidates(variant map[string]any) []string {
	var out []string
	var add func(any)
	add = func(value any) {
		switch item := value.(type) {
		case string:
			if strings.TrimSpace(item) != "" {
				out = append(out, item)
			}
		case []any:
			for _, nested := range item {
				add(nested)
			}
		}
	}
	for _, key := range []string{"main_url", "backup_url", "backup_url_1", "backup_url_2", "backup_url_list", "backup_urls", "url_list"} {
		add(variant[key])
	}
	return out
}

func hongguoPlayerMediaURL(candidate string, seed []byte) (string, error) {
	value := strings.TrimSpace(hongguoPlayerCleaner.Replace(candidate))
	if isProviderHTTPMediaURL(value) {
		return checkedHongguoPlayerMediaURL(value)
	}
	if len(value) > 8192 {
		return "", errors.New("地址过长")
	}
	raw, err := decodeHongguoBase64(value)
	if err != nil {
		return "", err
	}
	text := strings.TrimSpace(string(raw))
	if isProviderHTTPMediaURL(text) {
		return checkedHongguoPlayerMediaURL(text)
	}
	if len(raw) >= 5 && raw[0] == 0xa8 && raw[2] == 0x01 && raw[3] == 0x00 && len(seed) > 0 {
		decrypted, err := decryptHongguoSpadeURL(raw, seed)
		if err == nil && isProviderHTTPMediaURL(decrypted) {
			return checkedHongguoPlayerMediaURL(decrypted)
		}
	}
	return "", errors.New("无法识别的片源地址")
}

func checkedHongguoPlayerMediaURL(raw string) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.User != nil ||
		(parsed.Port() != "" && parsed.Port() != "443") {
		return "", errors.New("片源地址格式无效")
	}
	host := strings.ToLower(parsed.Hostname())
	for _, allowed := range hongguoPlayerMediaHosts {
		if host == allowed || strings.HasSuffix(host, "."+allowed) {
			return parsed.String(), nil
		}
	}
	return "", errors.New("片源地址不在可信域名内")
}

func decryptHongguoSpadeURL(raw, seed []byte) (string, error) {
	length := (len(raw) - 4) / 16 * 16
	if length <= 0 {
		return "", errors.New("片源密文长度无效")
	}
	salt, err := hex.DecodeString(hongguoDeviceKDFSalt)
	if err != nil {
		return "", err
	}
	inner := sha512.Sum512(seed)
	derived := sha512.Sum512(append(append([]byte(nil), inner[:]...), salt...))
	block, err := aes.NewCipher(derived[:16])
	if err != nil {
		return "", err
	}
	plain := make([]byte, length)
	cipher.NewCBCDecrypter(block, derived[16:32]).CryptBlocks(plain, raw[4:4+length])
	if padding := int(plain[len(plain)-1]); padding >= 1 && padding <= 16 && padding <= len(plain) {
		plain = plain[:len(plain)-padding]
	}
	return strings.TrimSpace(strings.TrimRight(string(plain), "\x00")), nil
}

func hongguoPlayerRenditionEncrypted(variant, encryptInfo map[string]any) bool {
	for _, flag := range []any{variant["encrypt"], encryptInfo["encrypt"]} {
		switch value := flag.(type) {
		case bool:
			if value {
				return true
			}
		case string:
			if value == "true" || value == "1" {
				return true
			}
		case json.Number:
			if value.String() != "" && value.String() != "0" {
				return true
			}
		}
	}
	method := strings.ToLower(firstNonEmpty(mapString(variant, "encryption_method"), mapString(encryptInfo, "encryption_method")))
	return strings.Contains(method, "cenc") || strings.Contains(method, "encrypt")
}

func hongguoPlayerContentKey(spade string) ([]byte, error) {
	if strings.TrimSpace(spade) == "" {
		return nil, nil
	}
	if key, err := hongguoContentKey(spade); err == nil {
		return key, nil
	}
	return deriveHongguoPlayerContentKey(spade)
}

func deriveHongguoPlayerContentKey(value string) ([]byte, error) {
	normalized := strings.NewReplacer("-", "+", "_", "/").Replace(strings.TrimSpace(value))
	raw, err := decodeHongguoBase64(normalized)
	if err != nil || len(raw) < 3 {
		return nil, errors.New("红果片源密钥编码无效")
	}
	length := len(raw) - int(raw[0]^raw[1]^raw[2]) + 47
	if length > len(raw)-1 {
		length = len(raw) - 1
	}
	if length < 33 {
		return nil, errors.New("红果片源密钥长度无效")
	}
	work := append([]byte(nil), raw[1:1+length]...)
	even, odd := uint8(246), uint8(85)
	for index := range work {
		current := work[index]
		previous := odd
		if index%2 == 0 {
			previous, even = even, current
		} else {
			previous, odd = odd, current
		}
		work[index] = byte(int(previous^current) - 21 - bits.OnesCount(uint(index)))
	}
	if length < 33 {
		return nil, errors.New("红果片源密钥内容无效")
	}
	key, err := hex.DecodeString(string(work[1:33]))
	if err != nil || len(key) != aes.BlockSize {
		return nil, errors.New("红果片源密钥不是有效的 AES-128 密钥")
	}
	return key, nil
}
