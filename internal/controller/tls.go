/*
Copyright 2026 Keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package controller

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	qdrantv1alpha1 "github.com/keiailab/qdrant-operator/api/v1alpha1"
	"github.com/keiailab/qdrant-operator/internal/qdrant"
	"github.com/keiailab/qdrant-operator/internal/resources"
)

// ── D-3 TLS ──
//
// spec.config.tlsEnabled 는 원래 있었지만 인증서를 아무도 주지 않았다 — 켜면 qdrant 가
// 기동하지 못하는 상태였다(qdrant 는 service/p2p TLS 가 켜지면 tls 섹션을 요구한다).
// 여기서 채우는 것은 그 구멍이다.
//
// 오퍼레이터는 인증서를 발급하지 않는다. cert-manager 가 만드는 Secret 모양이 기본값이고,
// 손으로 만든 것도 같은 모양이면 된다. 자체 CA 는 cert-manager 재발명이라 택하지 않았다.

// tlsEnabled 는 인증서까지 갖춰진 TLS 인지다. 플래그만 켜고 Secret 이 없는 상태는
// TLS 가 아니라 **깨진 설정**이므로 여기서 참이 아니다.
func tlsEnabled(qc *qdrantv1alpha1.QdrantCluster) bool {
	return qc.Spec.Config.TLSEnabled && qc.Spec.Config.TLS != nil && qc.Spec.Config.TLS.SecretName != ""
}

// tlsMisconfigured 는 TLS 를 켰는데 인증서가 없는지다 — apply 하면 CrashLoop 이 된다.
func tlsMisconfigured(qc *qdrantv1alpha1.QdrantCluster) bool {
	return qc.Spec.Config.TLSEnabled && !tlsEnabled(qc)
}

// clusterScheme 은 오퍼레이터가 클러스터에 붙을 때 쓸 스킴이다.
func clusterScheme(qc *qdrantv1alpha1.QdrantCluster) string {
	if tlsEnabled(qc) {
		return "https"
	}
	return "http"
}

// clientBaseURL / peerBaseURL 은 오퍼레이터가 쓰는 두 주소다.
func clientBaseURL(qc *qdrantv1alpha1.QdrantCluster) string {
	return fmt.Sprintf("%s://%s.%s.svc:%d", clusterScheme(qc),
		resources.ClientName(qc), qc.Namespace, resources.RESTPort)
}

func peerBaseURL(qc *qdrantv1alpha1.QdrantCluster, ordinal int32) string {
	return fmt.Sprintf("%s://%s-%d.%s.%s.svc:%d", clusterScheme(qc),
		qc.Name, ordinal, resources.HeadlessName(qc), qc.Namespace, resources.RESTPort)
}

// newClusterClient 는 TLS 여부에 맞는 클라이언트를 만든다.
//
// TLS 를 켠 순간 오퍼레이터가 http 로 남으면 관측이 전부 실패하고, 관측이 없으면
// 리밸런스·백업·업그레이드 게이트가 모두 멈춘다 — TLS 를 켜는 것이 오퍼레이터를 장님으로
// 만드는 셈이다. CA 는 서버가 쓰는 것과 **같은 Secret** 에서 읽는다.
//
// 읽기는 캐시 클라이언트가 아니라 reader(직행)로 한다. Secret 을 매니저 캐시에 올리면
// 범위 안의 모든 Secret 을 들고 있게 되고, 이 컨트롤러의 메모리 상한은 128Mi 다.
func newClusterClient(ctx context.Context, reader client.Reader, qc *qdrantv1alpha1.QdrantCluster, baseURL string) qdrant.Client {
	if !tlsEnabled(qc) {
		return qdrant.NewHTTPClient(baseURL)
	}

	ca, err := readCACert(ctx, reader, qc)
	if err != nil {
		// 여기서 http 로 되돌리면 검증 없는 접속이 조용한 기본값이 된다.
		// 붙지 못하는 클라이언트를 주고, 실패는 관측 실패로 표면화되게 둔다.
		return qdrant.NewHTTPClient(baseURL)
	}

	c, err := qdrant.NewHTTPSClient(baseURL, ca)
	if err != nil {
		return qdrant.NewHTTPClient(baseURL)
	}
	return c
}

// readCACert 는 인증서 Secret 에서 CA 를 꺼낸다.
func readCACert(ctx context.Context, reader client.Reader, qc *qdrantv1alpha1.QdrantCluster) ([]byte, error) {
	tls := qc.Spec.Config.TLS
	key := types.NamespacedName{Name: tls.SecretName, Namespace: qc.Namespace}

	secret := &corev1.Secret{}
	if err := reader.Get(ctx, key, secret); err != nil {
		return nil, err
	}

	caKey := tls.CACertKey
	if caKey == "" {
		caKey = resources.DefaultTLSCACertKey
	}
	ca, ok := secret.Data[caKey]
	if !ok || len(ca) == 0 {
		return nil, fmt.Errorf("secret %s 에 %q 가 없다", tls.SecretName, caKey)
	}
	return ca, nil
}
