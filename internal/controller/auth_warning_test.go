/*
Copyright 2026 Keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package controller

import (
	"testing"

	qdrantv1alpha1 "github.com/keiailab/qdrant-operator/api/v1alpha1"
)

// authWithoutTLS 진리표 — 인증 키가 설정됐고 TLS 가 꺼진 경우에만 경고 대상.
func TestAuthWithoutTLS(t *testing.T) {
	ref := &qdrantv1alpha1.SecretKeyRef{Name: "qdrant-auth", Key: "api-key"}
	cases := []struct {
		name          string
		apiKey, roKey *qdrantv1alpha1.SecretKeyRef
		tls           bool
		want          bool
	}{
		{"키없음+TLSoff", nil, nil, false, false},
		{"쓰기키+TLSoff", ref, nil, false, true},
		{"읽기키+TLSoff", nil, ref, false, true},
		{"둘다+TLSoff", ref, ref, false, true},
		{"쓰기키+TLSon", ref, nil, true, false},
		{"키없음+TLSon", nil, nil, true, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			qc := &qdrantv1alpha1.QdrantCluster{}
			qc.Spec.APIKey = c.apiKey
			qc.Spec.ReadOnlyAPIKey = c.roKey
			qc.Spec.Config.TLSEnabled = c.tls
			if got := authWithoutTLS(qc); got != c.want {
				t.Fatalf("authWithoutTLS=%v (want %v)", got, c.want)
			}
		})
	}
}
