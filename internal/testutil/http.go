package testutil

import "net/http"

// RoundTripFunc 允许在测试中 mock HTTP transport。
type RoundTripFunc func(req *http.Request) (*http.Response, error)

func (f RoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

// NewMockClient 返回使用指定 RoundTripFunc 的 http.Client。
func NewMockClient(fn RoundTripFunc) *http.Client {
	return &http.Client{Transport: fn}
}