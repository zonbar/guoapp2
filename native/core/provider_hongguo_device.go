package core

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"crypto/rand"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const hongguoDeviceRegisterURL = "https://log5-applog-lf.fqnovel.com/service/2/device_register/?tt_data=a&cronet_version=8d40f833_2026-03-03&ttnet_version=4.2.243.31-douyin&use_store_region_cookie=1"

const hongguoDeviceKDFSalt = "4dd4c2e6b83162090e52b3c7a6733ba41cb2462b829ab58a196b39db57177524f49baf7f08e8d68d26a72e37c1a95a2f1f05a51892aef2949732b62a38aadd58"

const hongguoPlaybackUserAgent = "com.phoenix.read/74332 (Linux; U; Android 16; zh_CN; 25053RT47C; Build/BP2A.250605.031.A3; Cronet/TTNetVersion:04657795 2026-01-23 QuicVersion:c67e9834 2025-09-08)"

const hongguoDeviceFileName = "hongguo-playback-device.json"

type hongguoPlaybackDevice struct {
	DeviceID  string `json:"deviceId"`
	InstallID string `json:"installId"`
	CDID      string `json:"cdid"`
	Cookie    string `json:"cookie"`
	CreatedAt int64  `json:"createdAt"`
}

func (device *hongguoPlaybackDevice) valid() bool {
	return device != nil && hongguoNumericID.MatchString(device.DeviceID) &&
		hongguoNumericID.MatchString(device.InstallID) && device.CDID != ""
}

func hongguoFallbackPlaybackDevice() *hongguoPlaybackDevice {
	return &hongguoPlaybackDevice{
		DeviceID:  "1624456208323876",
		InstallID: "1624456208327972",
		CDID:      "91d56f7e-c027-4ce9-9a3c-229d59a13c3b",
		Cookie:    "install_id=1624456208327972; store-region=cn-bj; store-region-src=did",
		CreatedAt: time.Now().UnixMilli(),
	}
}

func newHongguoUUID() string {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return ""
	}
	value[6] = value[6]&0x0f | 0x40
	value[8] = value[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", value[0:4], value[4:6], value[6:8], value[8:10], value[10:16])
}

func (d *Downloader) hongguoDeviceFile() string {
	if strings.TrimSpace(d.cfg.dataDir) == "" {
		return ""
	}
	return filepath.Join(d.cfg.dataDir, hongguoDeviceFileName)
}

func (d *Downloader) loadHongguoPlaybackDevice() *hongguoPlaybackDevice {
	path := d.hongguoDeviceFile()
	if path == "" {
		return nil
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var device hongguoPlaybackDevice
	if json.Unmarshal(body, &device) != nil || !device.valid() {
		return nil
	}
	if device.Cookie == "" {
		device.Cookie = hongguoDeviceCookie(device.InstallID, nil)
	}
	return &device
}

func (d *Downloader) saveHongguoPlaybackDevice(device *hongguoPlaybackDevice) {
	path := d.hongguoDeviceFile()
	if path == "" || !device.valid() {
		return
	}
	body, err := json.Marshal(device)
	if err != nil {
		return
	}
	_ = os.MkdirAll(filepath.Dir(path), 0o700)
	temporary := path + ".tmp"
	if os.WriteFile(temporary, body, 0o600) == nil {
		_ = os.Rename(temporary, path)
	}
}

func hongguoDeviceCookie(installID string, setCookies []string) string {
	parts := make([]string, 0, 3)
	for _, raw := range setCookies {
		first := strings.TrimSpace(strings.SplitN(raw, ";", 2)[0])
		if first == "" || strings.HasPrefix(strings.ToLower(first), "ttreq=") {
			continue
		}
		parts = append(parts, first)
	}
	joined := strings.Join(parts, "; ")
	if strings.Contains(joined, "install_id=") {
		return joined
	}
	if installID == "" {
		return joined
	}
	if joined == "" {
		return "store-region=cn-bj; store-region-src=did; install_id=" + installID
	}
	return joined + "; install_id=" + installID
}

func hongguoRegistrationBody(now int64, cdid string) ([]byte, error) {
	openudid := make([]byte, 8)
	if _, err := rand.Read(openudid); err != nil {
		return nil, err
	}
	header := map[string]any{
		"display_name": "红果免费短剧", "update_version_code": 74332, "manifest_version_code": 74332,
		"app_version_minor": "7.4.3.32", "aid": 8662, "channel": "update_64", "package": "com.phoenix.read",
		"app_version": "7.4.3.32", "version_code": 74332, "sdk_version": "3.7.0-rc.34-fanqie-xiaoshuo",
		"sdk_target_version": 29, "git_hash": "3a88f4e", "os": "Android", "os_version": "16", "os_api": 36,
		"device_model": "25053RT47C", "device_brand": "Redmi", "device_manufacturer": "Redmi",
		"cpu_abi": "arm64-v8a", "release_build": "b5e187f", "density_dpi": 520, "display_density": "mdpi",
		"resolution": "1280x2772", "language": "zh", "timezone": 8, "access": "wifi", "not_request_sender": 0,
		"carrier": "CHINA MOBILE", "mcc_mnc": "46000", "rom": "eng.root.20231116.160340",
		"rom_version": "W528JS release-keys", "cdid": cdid,
		"sig_hash": "56a962410c494bbaf0b58dba20cae56f", "openudid": hex.EncodeToString(openudid),
		"clientudid": newHongguoUUID(), "ipv6_list": []any{}, "region": "CN", "tz_name": "Asia/Shanghai",
		"tz_offset": 28800, "sim_region": "cn", "sim_serial_number": []any{}, "oaid_may_support": false,
		"req_id": newHongguoUUID(), "device_platform": "android",
		"custom":                map[string]any{"account_region_src": "fallback", "account_region": "cn", "host_bit": 64, "dragon_device_type": "phone"},
		"pre_installed_channel": "", "apk_first_install_time": now, "is_system_app": 0,
		"sdk_flavor": "china", "guest_mode": 0,
	}
	raw, err := json.Marshal(map[string]any{"magic_tag": "ss_app_log", "header": header, "_gen_time": now})
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
	random := make([]byte, 32)
	if _, err := rand.Read(random); err != nil {
		return nil, err
	}
	salt, err := hex.DecodeString(hongguoDeviceKDFSalt)
	if err != nil {
		return nil, err
	}
	inner := sha512.Sum512(random)
	derived := sha512.Sum512(append(append([]byte(nil), inner[:]...), salt...))
	digest := sha512.Sum512(compressed.Bytes())
	plaintext := append(append([]byte(nil), digest[:]...), compressed.Bytes()...)
	block, err := aes.NewCipher(derived[:16])
	if err != nil {
		return nil, err
	}
	padded := hongguoCommentPad(plaintext)
	cipher.NewCBCEncrypter(block, derived[16:32]).CryptBlocks(padded, padded)
	data := append([]byte{0x74, 0x63, 0x05, 0x10, 0x00, 0x00}, random...)
	data = append(data, padded...)
	return data, nil
}

func (d *Downloader) registerHongguoPlaybackDevice(ctx context.Context) (*hongguoPlaybackDevice, error) {
	now := time.Now().UnixMilli()
	cdid := newHongguoUUID()
	if cdid == "" {
		return nil, errors.New("无法生成红果播放设备标识")
	}
	body, err := hongguoRegistrationBody(now, cdid)
	if err != nil {
		return nil, err
	}
	endpoint := firstNonEmpty(d.cfg.HongguoRegisterURL, hongguoDeviceRegisterURL)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	stub := md5.Sum(body)
	request.Header.Set("User-Agent", hongguoPlaybackUserAgent)
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/octet-stream;tt-data=a")
	request.Header.Set("Log-Encode-Type", "gzip")
	request.Header.Set("X-SS-DP", "8662")
	request.Header.Set("X-SS-Req-Ticket", fmt.Sprintf("%d", now))
	request.Header.Set("X-VC-Bdturing-SDK-Version", "4.0.3.cn")
	request.Header.Set("Lc", "101")
	request.Header.Set("Sdk-Version", "2")
	request.Header.Set("Passport-Sdk-Version", "5051452")
	request.Header.Set("X-Neptune", "-8|50:51:59:30:40:47:49:39")
	request.Header.Set("X-SS-Stub", strings.ToUpper(hex.EncodeToString(stub[:])))
	response, err := d.doCatalogRequestWithTimeout(request, 12*time.Second)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	content, err := io.ReadAll(io.LimitReader(response.Body, 64<<10))
	if err != nil {
		return nil, err
	}
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("红果设备注册 HTTP %d", response.StatusCode)
	}
	var parsed map[string]any
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.UseNumber()
	if err := decoder.Decode(&parsed); err != nil {
		return nil, errors.New("红果设备注册返回格式异常")
	}
	if code := firstNonEmpty(mapString(parsed, "code", "Code"), "0"); code != "0" {
		return nil, fmt.Errorf("红果设备注册被拒绝（%s）", truncate(code, 20))
	}
	deviceID := firstNonEmpty(mapString(parsed, "device_id_str", "deviceIdStr"), mapString(parsed, "device_id", "deviceId"))
	installID := firstNonEmpty(mapString(parsed, "install_id_str", "installIdStr"), mapString(parsed, "install_id", "installId"))
	if !hongguoNumericID.MatchString(deviceID) || !hongguoNumericID.MatchString(installID) {
		return nil, errors.New("红果设备注册未返回有效标识")
	}
	cookies := response.Header.Values("Set-Cookie")
	device := &hongguoPlaybackDevice{
		DeviceID: deviceID, InstallID: installID, CDID: cdid,
		Cookie: hongguoDeviceCookie(installID, cookies), CreatedAt: now,
	}
	return device, nil
}

func (d *Downloader) refreshHongguoPlaybackDevice(ctx context.Context) *hongguoPlaybackDevice {
	client := d.hongguoClient()
	client.playbackMu.Lock()
	defer client.playbackMu.Unlock()
	device, err := d.registerHongguoPlaybackDevice(ctx)
	if err != nil {
		device = hongguoFallbackPlaybackDevice()
	} else {
		d.saveHongguoPlaybackDevice(device)
	}
	client.playbackDevice = device
	return device
}

func (d *Downloader) hongguoPlaybackDevice(ctx context.Context) *hongguoPlaybackDevice {
	client := d.hongguoClient()
	client.playbackMu.Lock()
	defer client.playbackMu.Unlock()
	if client.playbackDevice != nil {
		return client.playbackDevice
	}
	if device := d.loadHongguoPlaybackDevice(); device != nil {
		client.playbackDevice = device
		return device
	}
	device, err := d.registerHongguoPlaybackDevice(ctx)
	if err != nil {
		device = hongguoFallbackPlaybackDevice()
	} else {
		d.saveHongguoPlaybackDevice(device)
	}
	client.playbackDevice = device
	return device
}
