/*
Copyright 2026 Keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package qdrant

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"time"
)

// NewHTTPSClient 는 CA 로 서버를 검증하는 클라이언트를 만든다.
//
// TLS 를 켜면 오퍼레이터도 https 로 붙어야 한다. 그러지 않으면 관측이 전부 실패하고,
// 관측이 없으면 리밸런스·백업·업그레이드 게이트가 모두 멈춘다 — TLS 를 켠 순간 오퍼레이터가
// 장님이 되는 셈이다. 검증을 끄는 선택지는 두지 않는다: 내부 트래픽이라도 끄고 나면
// 그것이 기본값이 된다.
func NewHTTPSClient(baseURL string, caPEM []byte) (*HTTPClient, error) {
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("CA 인증서를 읽을 수 없다(PEM 아님)")
	}

	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}

	return &HTTPClient{
		BaseURL: baseURL,
		HC:      &http.Client{Timeout: 15 * time.Second, Transport: transport},
	}, nil
}
