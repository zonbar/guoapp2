package core

import (
	"net/http"
	"net/url"
	"testing"
)

func TestShouldUseSplitProxy(t *testing.T) {
	cases := []struct {
		host string
		want bool
	}{
		{"huangguo.video", true},
		{"HUANGGUO.VIDEO", true},
		{"huangguo.video.", true},
		{"img.huangguo.video", true},
		{"huangguoai.com", true},
		{"www.huangguoai.com", true},
		{"www.dsd.com.se", true},
		{"dsd.com.se", true},
		{"huangguo.video.evil.com", false},
		{"evilhuangguo.video", false},
		{"not-dsd.com.se", false},
		{"www.duanju2.com", false},
		{"ptt.red", false},
		{"pic.drleao.cn", false},
		{"127.0.0.1", false},
		{"", false},
		{"   ", false},
	}
	for _, item := range cases {
		if got := shouldUseSplitProxy(item.host); got != item.want {
			t.Errorf("shouldUseSplitProxy(%q) = %v, want %v", item.host, got, item.want)
		}
	}
}

func TestProxyRouterSplitMode(t *testing.T) {
	manual, err := url.Parse("http://127.0.0.1:1087")
	if err != nil {
		t.Fatal(err)
	}
	router := &proxyRouter{mode: "split", manual: manual}
	cases := []struct {
		rawurl string
		want   string
	}{
		{"https://huangguo.video/videos", "http://127.0.0.1:1087"},
		{"https://huangguo.video/uploads/lib/series/1/cover.webp", "http://127.0.0.1:1087"},
		{"https://img.huangguo.video/a.webp", "http://127.0.0.1:1087"},
		{"https://huangguoai.com/api/videos", "http://127.0.0.1:1087"},
		{"https://www.dsd.com.se/", "http://127.0.0.1:1087"},
		{"https://www.duanju2.com/", ""},
		{"https://ptt.red/p/66/c/67", ""},
		{"https://www.xzyx168.com/xzyxvt/1zmn.html", ""},
		{"https://huangguo.video.evil.com/x", ""},
		{"https://127.0.0.1:8080/local", ""},
	}
	for _, item := range cases {
		request, err := http.NewRequest(http.MethodGet, item.rawurl, nil)
		if err != nil {
			t.Fatal(err)
		}
		got, err := router.proxy(request)
		if err != nil {
			t.Fatalf("proxy(%s): %v", item.rawurl, err)
		}
		value := ""
		if got != nil {
			value = got.String()
		}
		if value != item.want {
			t.Errorf("proxy(%s) = %q, want %q", item.rawurl, value, item.want)
		}
	}
}

func TestProxyRouterSplitModeWithoutProxyURL(t *testing.T) {
	router := &proxyRouter{mode: "split"}
	request, err := http.NewRequest(http.MethodGet, "https://huangguo.video/videos", nil)
	if err != nil {
		t.Fatal(err)
	}
	got, err := router.proxy(request)
	if err != nil || got != nil {
		t.Fatalf("未配置代理地址时应直连，got %v, err %v", got, err)
	}
}

func TestProxyRouterOtherModesUnchanged(t *testing.T) {
	manual, _ := url.Parse("http://127.0.0.1:1087")
	request, err := http.NewRequest(http.MethodGet, "https://www.duanju2.com/", nil)
	if err != nil {
		t.Fatal(err)
	}
	direct := &proxyRouter{mode: "direct", manual: manual}
	if got, _ := direct.proxy(request); got != nil {
		t.Fatalf("direct 不应使用代理，got %v", got)
	}
	global := &proxyRouter{mode: "manual", manual: manual}
	got, err := global.proxy(request)
	if err != nil || got == nil || got.String() != manual.String() {
		t.Fatalf("manual 应始终使用代理，got %v, err %v", got, err)
	}
}

func TestResourceSettingsValidateSplitMode(t *testing.T) {
	cases := []struct {
		name    string
		mode    string
		address string
		wantErr bool
	}{
		{"分流缺地址", "split", "", true},
		{"分流合法地址", "split", "http://127.0.0.1:1087", false},
		{"分流 socks5", "split", "socks5://127.0.0.1:1080", false},
		{"分流非法地址", "split", "not-a-proxy", true},
		{"手动代理缺地址", "manual", "", true},
		{"自动无地址", "auto", "", false},
		{"直连无地址", "direct", "", false},
		{"未知模式", "bogus", "http://127.0.0.1:1087", true},
	}
	for _, item := range cases {
		settings := defaultResourceSettings()
		settings.ProxyMode, settings.ProxyURL = item.mode, item.address
		err := settings.validate()
		if (err != nil) != item.wantErr {
			t.Errorf("%s: validate() = %v, wantErr %v", item.name, err, item.wantErr)
		}
	}
}
