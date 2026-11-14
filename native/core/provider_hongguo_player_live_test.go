package core

import (
	"context"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

type hongguoLiveEpisode struct {
	number int
	vid    string
}

var hongguoLiveEpisodes = []hongguoLiveEpisode{
	{1, "7703410922106145816"},
	{6, "7703411040372919320"},
	{30, "7703410954578431000"},
	{63, "7703410992205532184"},
}

func TestHongguoPlayerLiveFullEpisodeRange(t *testing.T) {
	if os.Getenv("HONGGUO_LIVE") == "" {
		t.Skip("真实接口验证需设置 HONGGUO_LIVE=1")
	}
	episodes := hongguoLiveEpisodes
	if custom := strings.TrimSpace(os.Getenv("HONGGUO_VIDS")); custom != "" {
		episodes = nil
		for index, raw := range strings.Split(custom, ",") {
			if vid := strings.TrimSpace(raw); vid != "" {
				episodes = append(episodes, hongguoLiveEpisode{number: index + 1, vid: vid})
			}
		}
	}
	d := &Downloader{
		cfg:           Config{dataDir: t.TempDir(), Retries: 1},
		client:        &http.Client{Timeout: 25 * time.Second},
		limiter:       newRequestLimiter(3, 0),
		providerHosts: map[string]string{},
	}
	t.Logf("playback device=%+v", d.hongguoPlaybackDevice(context.Background()))
	for _, episode := range episodes {
		t.Run("episode_"+strconv.Itoa(episode.number), func(t *testing.T) {
			media, err := d.resolveHongguoPlayerMedia(context.Background(), episode.vid)
			if err != nil {
				t.Fatalf("第 %d 集取流失败: %v", episode.number, err)
			}
			if !isProviderHTTPMediaURL(media.URL) {
				t.Fatalf("第 %d 集片源地址无效: %q", episode.number, media.URL)
			}
			if len(media.CENCKey) != 0 && len(media.CENCKey) != 16 {
				t.Fatalf("第 %d 集密钥长度异常: %d", episode.number, len(media.CENCKey))
			}
			if media.Quality <= 0 || len(media.Variants) == 0 {
				t.Fatalf("第 %d 集缺少可用清晰度: quality=%d variants=%d", episode.number, media.Quality, len(media.Variants))
			}
			t.Logf("第 %d 集 quality=%d variants=%d key=%d duration=%s", episode.number,
				media.Quality, len(media.Variants), len(media.CENCKey), media.Duration)
		})
	}
}
