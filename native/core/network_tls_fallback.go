package core

import (
	"crypto/tls"
	"net/http"
	"strconv"
	"strings"
	"sync"
)

// 部分采集站使用过期证书或错误绑定域名的证书，另有少数服务端会拒绝客户端
// 提供的 ALPN 列表。这里只在请求确实因此失败时，针对该主机降级重试一次；
// 正常请求一律走原链路，避免整体放宽 TLS 校验或全局关闭 HTTP/2。
type tlsFallbackMode int

const (
	tlsFallbackNone tlsFallbackMode = iota
	tlsFallbackInsecure
	tlsFallbackHTTP1
)

type tlsFallbackTransport struct {
	base     http.RoundTripper
	raw      *http.Transport
	router   *proxyRouter
	mu       sync.Mutex
	degraded map[string]*http.Transport
}

func newTLSFallbackTransport(base http.RoundTripper, raw *http.Transport, router *proxyRouter) *tlsFallbackTransport {
	return &tlsFallbackTransport{base: base, raw: raw, router: router,
		degraded: make(map[string]*http.Transport)}
}

func classifyTLSError(err error) tlsFallbackMode {
	if err == nil {
		return tlsFallbackNone
	}
	message := strings.ToLower(err.Error())
	switch {
	case strings.Contains(message, "certificate has expired"),
		strings.Contains(message, "certificate is not yet valid"),
		strings.Contains(message, "certificate signed by unknown authority"),
		strings.Contains(message, "certificate is valid for"),
		strings.Contains(message, "x509:"):
		return tlsFallbackInsecure
	case strings.Contains(message, "no application protocol"),
		strings.Contains(message, "tls: handshake failure"):
		return tlsFallbackHTTP1
	}
	return tlsFallbackNone
}

func (transport *tlsFallbackTransport) degradedTransport(host string, mode tlsFallbackMode) *http.Transport {
	key := strings.ToLower(host) + "|" + strconv.Itoa(int(mode))
	transport.mu.Lock()
	defer transport.mu.Unlock()
	if existing := transport.degraded[key]; existing != nil {
		return existing
	}
	degraded := transport.raw.Clone()
	config := &tls.Config{MinVersion: tls.VersionTLS12}
	if transport.raw.TLSClientConfig != nil {
		config = transport.raw.TLSClientConfig.Clone()
	}
	switch mode {
	case tlsFallbackInsecure:
		config.InsecureSkipVerify = true
	case tlsFallbackHTTP1:
		config.NextProtos = []string{"http/1.1"}
		degraded.ForceAttemptHTTP2 = false
	}
	degraded.TLSClientConfig = config
	if transport.router != nil {
		degraded.Proxy = transport.router.proxy
	}
	transport.degraded[key] = degraded
	return degraded
}

func (transport *tlsFallbackTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	response, err := transport.base.RoundTrip(request)
	if err == nil {
		return response, nil
	}
	mode := classifyTLSError(err)
	if mode == tlsFallbackNone {
		return nil, err
	}
	// 请求体必须可重放，否则不能安全地重试。
	if request.Body != nil && request.GetBody == nil {
		return nil, err
	}
	retry := request.Clone(request.Context())
	if request.Body != nil {
		body, bodyErr := request.GetBody()
		if bodyErr != nil {
			return nil, err
		}
		retry.Body = body
	}
	degraded := transport.degradedTransport(request.URL.Hostname(), mode)
	result, retryErr := degraded.RoundTrip(retry)
	if retryErr != nil {
		if result != nil && result.Body != nil {
			result.Body.Close()
		}
		return nil, err
	}
	return result, nil
}

func (transport *tlsFallbackTransport) CloseIdleConnections() {
	if closer, ok := transport.base.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
	transport.mu.Lock()
	defer transport.mu.Unlock()
	for key, degraded := range transport.degraded {
		degraded.CloseIdleConnections()
		delete(transport.degraded, key)
	}
}

func isTransientNetworkError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	for _, fragment := range []string{
		"connection reset by peer", "unexpected eof", " eof", "i/o timeout",
		"connection refused", "no such host", "network is unreachable",
	} {
		if strings.Contains(message, fragment) {
			return true
		}
	}
	return false
}
