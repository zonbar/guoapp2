package core

import (
	"crypto/tls"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type fallbackCountingTransport struct {
	inner http.RoundTripper
	calls int
}

func (transport *fallbackCountingTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	transport.calls++
	return transport.inner.RoundTrip(request)
}

func newFallbackTestClient(raw *http.Transport) (*http.Client, *fallbackCountingTransport, *tlsFallbackTransport) {
	counting := &fallbackCountingTransport{inner: raw}
	fallback := newTLSFallbackTransport(counting, raw, nil)
	return &http.Client{Transport: fallback}, counting, fallback
}

func TestTLSFallbackRetriesCertificateError(t *testing.T) {
	hits := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		_, _ = w.Write([]byte("ok"))
	}))
	defer server.Close()

	raw := &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}}
	client, counting, fallback := newFallbackTestClient(raw)
	response, err := client.Get(server.URL)
	if err != nil {
		t.Fatalf("自签证书应触发降级后成功: %v", err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if string(body) != "ok" {
		t.Fatalf("响应内容 = %q, want ok", body)
	}
	if hits != 1 || counting.calls != 1 {
		t.Fatalf("期望原链路 1 次、降级链路 1 次请求，got base=%d handler=%d", counting.calls, hits)
	}
	if len(fallback.degraded) != 1 {
		t.Fatalf("应按主机缓存降级 transport，got %d", len(fallback.degraded))
	}
}

func TestTLSFallbackReplaysPostBody(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(request.Body)
		_, _ = w.Write(body)
	}))
	defer server.Close()

	raw := &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}}
	client, _, _ := newFallbackTestClient(raw)
	request, err := http.NewRequest(http.MethodPost, server.URL, strings.NewReader("payload-42"))
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("POST 降级重试失败: %v", err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if string(body) != "payload-42" {
		t.Fatalf("重试后请求体 = %q, want payload-42", body)
	}
}

func TestTLSFallbackIgnoresUnrelatedErrors(t *testing.T) {
	raw := &http.Transport{}
	client, counting, fallback := newFallbackTestClient(raw)
	_, err := client.Get("http://127.0.0.1:1/unreachable")
	if err == nil {
		t.Fatal("预期连接失败")
	}
	if counting.calls != 1 {
		t.Fatalf("非 TLS 错误不应重试，got %d 次", counting.calls)
	}
	if len(fallback.degraded) != 0 {
		t.Fatalf("非 TLS 错误不应创建降级 transport，got %d", len(fallback.degraded))
	}
}

func TestClassifyTLSError(t *testing.T) {
	cases := []struct {
		message string
		want    tlsFallbackMode
	}{
		{"Get \"https://x/\": x509: certificate has expired", tlsFallbackInsecure},
		{"tls: failed to verify certificate: x509: certificate signed by unknown authority", tlsFallbackInsecure},
		{"x509: certificate is valid for a.example, not b.example", tlsFallbackInsecure},
		{"remote error: tls: no application protocol", tlsFallbackHTTP1},
		{"remote error: tls: handshake failure", tlsFallbackHTTP1},
		{"read tcp: connection reset by peer", tlsFallbackNone},
		{"context deadline exceeded", tlsFallbackNone},
		{"", tlsFallbackNone},
	}
	for _, item := range cases {
		if got := classifyTLSError(errors.New(item.message)); got != item.want {
			t.Errorf("classifyTLSError(%q) = %v, want %v", item.message, got, item.want)
		}
	}
	if got := classifyTLSError(nil); got != tlsFallbackNone {
		t.Errorf("classifyTLSError(nil) = %v, want none", got)
	}
}

func TestIsTransientNetworkError(t *testing.T) {
	cases := []struct {
		message string
		want    bool
	}{
		{"read tcp 1.2.3.4: read: connection reset by peer", true},
		{"unexpected EOF", true},
		{"i/o timeout", true},
		{"HTTP 403", false},
		{"context deadline exceeded", false},
		{"", false},
	}
	for _, item := range cases {
		if got := isTransientNetworkError(errors.New(item.message)); got != item.want {
			t.Errorf("isTransientNetworkError(%q) = %v, want %v", item.message, got, item.want)
		}
	}
	if isTransientNetworkError(nil) {
		t.Error("isTransientNetworkError(nil) 应为 false")
	}
}
