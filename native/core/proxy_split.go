package core

import "strings"

// splitProxyHosts 是需要经代理访问的域名根。直连环境下这些站点存在 DNS 污染、
// TLS 握手超时或出口风控；而国内多数站点走代理反而会触发浏览器验证 403。
// 匹配时同时覆盖其子域，新增线路只需在此补充根域名。
var splitProxyHosts = []string{
	"huangguo.video",
	"huangguoai.com",
	"dsd.com.se",
	// 野果线路域名由中转页动态下发，直连会被连接重置，只有经代理可取到接口脚本。
	"buxefaex.cc",
	"fzchosdi.cc",
	// 花果/网果等共用的媒体分片域名，直连稳定超时，经代理可正常返回 206。
	"adfg8.vip",
}

func shouldUseSplitProxy(host string) bool {
	host = strings.ToLower(strings.TrimSpace(strings.TrimSuffix(host, ".")))
	if host == "" {
		return false
	}
	for _, allowed := range splitProxyHosts {
		if host == allowed || strings.HasSuffix(host, "."+allowed) {
			return true
		}
	}
	return false
}
