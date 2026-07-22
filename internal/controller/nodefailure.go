package controller

import (
	"context"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	commonsevents "github.com/keiailab/keiailab-commons/pkg/events"
	qdrantv1alpha1 "github.com/keiailab/qdrant-operator/api/v1alpha1"
	resources "github.com/keiailab/qdrant-operator/internal/resources"
)

// nodeFailureGrace — 파드 toleration(not-ready/unreachable 300s) 만료 + 여유.
// 이 시간 이상 노드가 NotReady(또는 부재)면 그 노드의 파드는 되살아날 가망이 없다고 본다.
const nodeFailureGrace = 6 * time.Minute

// reconcileStuckPods 는 죽은 노드에 갇힌 자기 STS 파드를 강제 삭제해 StatefulSet 이
// 대체 파드를 만들 수 있게 한다(설계 docs/design/node-failure-recovery-design.md).
//
// 배경: 노드가 영구 이탈하면 kubelet 부재로 파드 삭제가 확정되지 않고, StatefulSet 은
// ordinal 단일성 보장 때문에 대체 파드를 만들지 않는다 — 서비스는 RF>=2 로 지속되나
// replicas 가 조용히 줄어 HA 가 소실된다. 노드 taint(out-of-service)는 그 노드의 모든
// 워크로드를 축출하는 전역 조치라, 오퍼레이터는 자기 파드만 정리한다(영향 국소화).
//
// 반환: 삭제한 파드 수(0 또는 1 — 동시 1개 상한).
func (r *QdrantClusterReconciler) reconcileStuckPods(ctx context.Context, qc *qdrantv1alpha1.QdrantCluster) int {
	if qc.Spec.Replicas < 2 {
		// 단일 파드는 대체 파드도 같은 노드 상황에 놓여 무의미 — 무행동이 안전하다.
		return 0
	}
	pods := &corev1.PodList{}
	if err := r.List(ctx, pods, client.InNamespace(qc.Namespace), client.MatchingLabels(resources.SelectorLabels(qc))); err != nil {
		return 0
	}
	for i := range pods.Items {
		pod := &pods.Items[i]
		if pod.Spec.NodeName == "" {
			continue // 아직 스케줄 전 — 노드 장애와 무관
		}
		down, since := r.nodeDown(ctx, pod.Spec.NodeName)
		if !down || time.Since(since) < nodeFailureGrace {
			continue
		}
		// 후보 확정 — 동시 1개만 정리하고 다음 cycle 로 넘긴다(연쇄 축출로 정족수 파괴 방지).
		grace := int64(0)
		if err := r.Delete(ctx, pod, &client.DeleteOptions{GracePeriodSeconds: &grace}); err != nil && !apierrors.IsNotFound(err) {
			return 0
		}
		commonsevents.EmitWarningf(r.Recorder, qc, "StuckPodDeleted",
			"%s", pod.Name+" 가 죽은 노드 "+pod.Spec.NodeName+" 에 갇혀 강제 삭제 — StatefulSet 이 대체 파드를 생성한다")
		return 1
	}
	return 0
}

// nodeDown 은 노드가 NotReady(또는 부재)인지와 그 상태의 시작 시각을 돌려준다.
// 노드 객체가 사라진 경우(클러스터에서 제거)는 즉시 장애로 보고 파드 생성 시각을 기준
// 시점으로 쓴다 — 파드가 그보다 오래됐다면 이미 grace 를 넘긴 것이다.
func (r *QdrantClusterReconciler) nodeDown(ctx context.Context, nodeName string) (bool, time.Time) {
	node := &corev1.Node{}
	if err := r.Get(ctx, types.NamespacedName{Name: nodeName}, node); err != nil {
		if apierrors.IsNotFound(err) {
			return true, time.Time{} // zero time → time.Since 가 매우 커서 grace 즉시 충족
		}
		return false, time.Time{} // 조회 실패는 장애로 단정하지 않는다(false negative 우선)
	}
	for _, c := range node.Status.Conditions {
		if c.Type == corev1.NodeReady {
			if c.Status == corev1.ConditionTrue {
				return false, time.Time{}
			}
			return true, c.LastTransitionTime.Time
		}
	}
	return false, time.Time{} // Ready condition 부재 = 판단 불가 → 무행동
}
