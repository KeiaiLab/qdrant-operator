/*
Copyright 2026 Keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

// Package rbac 는 생성된 ClusterRole(config/rbac/role.yaml)과 차트가 손으로 미러한
// ClusterRole(deploy/chart/templates/rbac.yaml)의 권한을 고정한다.
//
// 왜 있나(2026-11-07 실측, ns data): 오퍼레이터 로그 `Server rejected event (will not retry!)`
// — SA data:qdrant-operator-controller-manager 가 events.k8s.io events 를 patch 할 수 없어
// 스냅샷 프루닝 이벤트가 전부 버려졌다. ClusterRole 에 events 규칙이 하나도 없었다.
package rbac

import (
	"os"
	"regexp"
	"strings"
	"testing"

	rbacv1 "k8s.io/api/rbac/v1"
	"sigs.k8s.io/yaml"
)

const (
	generatedRolePath = "../../config/rbac/role.yaml"
	chartRBACPath     = "../../deploy/chart/templates/rbac.yaml"
	// chartManagerRoleName 은 차트 템플릿의 manager ClusterRole 이름 접미(렌더링 전).
	chartManagerRoleName = "-manager-role"
)

// 렌더링 없이 YAML 로 읽기 위해 템플릿 지시자를 걷어낸다 — 한 줄짜리는 삭제,
// 값 안의 것(`name: {{ include … }}-manager-role`)은 자리표시자로 바꾼다.
var (
	helmDirectiveLine   = regexp.MustCompile(`(?m)^\s*\{\{.*\}\}\s*$\n?`)
	helmDirectiveInline = regexp.MustCompile(`\{\{.*?\}\}`)
)

const releasePlaceholder = "RELEASE"

var docSeparator = regexp.MustCompile(`(?m)^---\s*$`)

// permission 은 (group, resource, verb) 하나 — 규칙 비교의 최소 단위.
type permission struct{ group, resource, verb string }

func flatten(rules []rbacv1.PolicyRule) map[permission]bool {
	out := map[permission]bool{}
	for _, r := range rules {
		for _, g := range r.APIGroups {
			for _, res := range r.Resources {
				for _, v := range r.Verbs {
					out[permission{g, res, v}] = true
				}
			}
		}
	}
	return out
}

func loadGeneratedRole(t *testing.T) *rbacv1.ClusterRole {
	t.Helper()
	raw, err := os.ReadFile(generatedRolePath)
	if err != nil {
		t.Fatalf("read %s: %v", generatedRolePath, err)
	}
	role := &rbacv1.ClusterRole{}
	if err := yaml.Unmarshal(raw, role); err != nil {
		t.Fatalf("decode %s: %v", generatedRolePath, err)
	}
	return role
}

// loadChartManagerRole 은 차트 rbac.yaml 에서 manager ClusterRole 문서만 골라 디코드한다.
// 템플릿 지시자 줄만 걷어내면 나머지는 평문 YAML 이다(metadata.name 은 접미로 식별).
func loadChartManagerRole(t *testing.T) *rbacv1.ClusterRole {
	t.Helper()
	raw, err := os.ReadFile(chartRBACPath)
	if err != nil {
		t.Fatalf("read %s: %v", chartRBACPath, err)
	}
	for _, doc := range docSeparator.Split(string(raw), -1) {
		if !strings.Contains(doc, "kind: ClusterRole\n") {
			continue
		}
		plain := helmDirectiveLine.ReplaceAllString(doc, "")
		plain = helmDirectiveInline.ReplaceAllString(plain, releasePlaceholder)
		role := &rbacv1.ClusterRole{}
		if err := yaml.Unmarshal([]byte(plain), role); err != nil {
			t.Fatalf("decode chart ClusterRole: %v\n%s", err, doc)
		}
		if strings.HasSuffix(role.Name, chartManagerRoleName) {
			return role
		}
	}
	t.Fatalf("%s: no ClusterRole named *%s", chartRBACPath, chartManagerRoleName)
	return nil
}

// TestGeneratedRoleAllowsEvents — 이벤트 레코더는 core 와 events.k8s.io 양쪽 API 로 쓴다.
func TestGeneratedRoleAllowsEvents(t *testing.T) {
	have := flatten(loadGeneratedRole(t).Rules)
	for _, group := range []string{"", "events.k8s.io"} {
		for _, verb := range []string{"create", "patch"} {
			p := permission{group, "events", verb}
			if !have[p] {
				t.Errorf("config/rbac/role.yaml lacks %+v", p)
			}
		}
	}
}

// TestChartRoleCoversGeneratedRole — 차트는 생성물의 모든 권한을 담아야 한다(손 미러 드리프트 방지).
func TestChartRoleCoversGeneratedRole(t *testing.T) {
	chart := flatten(loadChartManagerRole(t).Rules)
	for p := range flatten(loadGeneratedRole(t).Rules) {
		if !chart[p] {
			t.Errorf("deploy/chart/templates/rbac.yaml lacks %+v", p)
		}
	}
}
