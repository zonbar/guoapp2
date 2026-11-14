package core

import (
	"encoding/hex"
	"testing"
)

const hongguoPlayerVectorQuery = "iid=4140140237700394&device_id=4140140237696298&aid=1967&app_name=novelapp&version_code=72132&version_name=7.2.1.32&device_platform=android&device_brand=Xiaomi&os_version=13&cdid=13f6f70e-e7be-4d0d-8169-fe4be0b2780d"

func TestHongguoPlayerSignVectors(t *testing.T) {
	const wantLadon = "ESIzRLIq1iuyfQl+Iog5lfzQMwsUr0z1fIm4duijgcZOk7s+"
	const wantArgus = "8oGwZRLe2V38m+1gfqIeLa2MoV9ZBWosW5Y0NXKHAB2aEbDn3BiXWD9oZwQR63xV0I836IYMfT1Z3s6gR0BHStgwgLUTqkN9xYmVBZEqd1mLRDaYbvcwELymn0HN5UFTwW1wekMhrfUGIkBFHV29eQPp3tCLv6DkohFhP7uGmJrtFNB22Yb6f2RUM/kkShih7uy6oXhw4K4JMUMrBum6oujq2qEaRwdhquMEn5J4yrkwuNRWAj9Q1V3LEJqzzHT7crSkWQucRHnuRvpEoNkQ7hpNG1Tb5z2bjEDfsRJdXLY+AA=="
	const timestamp = 1794655319

	if got := hongguoPlayerLadon(timestamp, "1611921764", "1967", []byte{0x11, 0x22, 0x33, 0x44}); got != wantLadon {
		t.Fatalf("Ladon 签名与参考实现不一致: %s, want %s", got, wantLadon)
	}
	if got := hongguoPlayerArgus(hongguoPlayerVectorQuery, "add92e5442be67700a0d43106cb75cb6", timestamp, "1967", "4140140237696298", "4140140237700394", 123456789); got != wantArgus {
		t.Fatalf("Argus 签名与参考实现不一致: %s, want %s", got, wantArgus)
	}
}

func TestHongguoPlayerSM3Vectors(t *testing.T) {
	if got := hex.EncodeToString(arrayToSlice(hongguoSM3(make([]byte, 16)))); got != "106e34a2b8c7bb13156cfdd0d91379dcc47543dcf9787c68ae5eb582620ae6e8" {
		t.Fatalf("SM3 空输入向量不一致: %s", got)
	}
	if got := hex.EncodeToString(arrayToSlice(hongguoSM3([]byte(hongguoPlayerVectorQuery)))); got != "e1d11ffd45dcfb2903b3a8988cbfe6dd691c1c88faffe991e49defa6be35bbd0" {
		t.Fatalf("SM3 query 向量不一致: %s", got)
	}
}

func arrayToSlice(value [32]byte) []byte { return value[:] }

func TestHongguoPlayerEncodeQuery(t *testing.T) {
	got := hongguoPlayerEncodeQuery([][2]string{
		{"resolution", "1280*2772"},
		{"keyword", "短剧 A!"},
		{"channel", "update_64"},
	})
	want := "resolution=1280*2772&keyword=%E7%9F%AD%E5%89%A7%20A%21&channel=update_64"
	if got != want {
		t.Fatalf("query 编码不一致: %s, want %s", got, want)
	}
}

func TestHongguoPlayerContentKeyVector(t *testing.T) {
	key, err := hongguoPlayerContentKey("nbwTwFiPEsNDhT7McIAh+nGyP/h1szj+QrUN/XK3Ds1xtjm+vg==")
	if err != nil {
		t.Fatalf("密钥派生失败: %v", err)
	}
	if got := hex.EncodeToString(key); got != "0f493590f2659b72732455a470f2caee" {
		t.Fatalf("内容密钥不一致: %s", got)
	}
}
