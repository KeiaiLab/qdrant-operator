/*
Copyright 2026 Keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package qdrant

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestNewHTTPSClient_CA검증(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"result":{"collections":[{"name":"vec"}]},"status":"ok"}`))
	}))
	defer srv.Close()

	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})

	c, err := NewHTTPSClient(srv.URL, caPEM)
	if err != nil {
		t.Fatal(err)
	}
	got, err := c.ListCollections(context.Background())
	if err != nil {
		t.Fatalf("CA 를 줬는데 검증 실패: %v", err)
	}
	if len(got) != 1 || got[0] != "vec" {
		t.Fatalf("응답: %+v", got)
	}
}

func TestNewHTTPSClient_잘못된CA는거부(t *testing.T) {
	// PEM 이 아니면 조용히 통과시키지 않는다 — 그러면 검증 없는 클라이언트가 된다.
	if _, err := NewHTTPSClient("https://x", []byte("not a pem")); err == nil {
		t.Fatal("PEM 아닌 CA 를 받아들였다")
	}
}

func TestNewHTTPSClient_다른CA는실패해야한다(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	// 주의: httptest.NewTLSServer 는 **모든 서버가 같은 내장 인증서**를 쓴다. 서버를 하나 더
	// 띄워 그 인증서를 "남의 CA" 로 쓰면 사실 같은 CA 라 검증이 통과해 버린다(실제로 겪었다).
	// 무관한 CA 를 직접 만들어야 이 케이스가 의미를 갖는다.
	foreignPEM := selfSignedCA(t)

	c, err := NewHTTPSClient(srv.URL, foreignPEM)
	if err != nil {
		t.Fatal(err)
	}

	// 이 단언이 없으면 RootCAs 를 안 걸어도 앞 테스트가 통과해 버린다 — 검증이 실제로
	// 도는지 확인하는 것이 이 케이스의 존재 이유다.
	_, err = c.ListCollections(context.Background())
	if err == nil {
		t.Fatal("남의 CA 로 서명된 서버를 받아들였다")
	}
	var verr *tls.CertificateVerificationError
	if !errors.As(err, &verr) {
		t.Fatalf("인증서 검증 실패가 아닌 다른 이유로 실패: %v", err)
	}
}

// selfSignedCA 는 이 테스트에서만 쓰는 무관한 CA 인증서를 만든다.
func selfSignedCA(t *testing.T) []byte {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "unrelated-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}
