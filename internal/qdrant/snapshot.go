/*
Copyright 2026 Keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package qdrant

import (
	"context"
	"net/url"
)

// ── C-1 스냅샷 API ──
//
// 스냅샷은 **노드 단위**다. 분산 클러스터에서 POST /collections/{c}/snapshots 는 요청을 받은
// peer 가 가진 shard 만 담는다. 컬렉션 하나의 온전한 백업은 전 peer 의 스냅샷 집합이며,
// 그 팬아웃은 컨트롤러 몫이다(QdrantClientForPeer 재사용).
//
// 보관 위치는 이 API 가 정하지 않는다 — spec.snapshots 가 배선한 qdrant 설정이 정한다.
// S3 보관이면 같은 호출이 오브젝트 스토리지에 쓴다. 오퍼레이터는 바이트를 만지지 않는다.

// SnapshotInfo 는 스냅샷 하나의 관측이다.
type SnapshotInfo struct {
	Name         string `json:"name"`
	CreationTime string `json:"creation_time"` // qdrant 가 비워 보낼 수 있다
	Size         uint64 `json:"size"`
}

// CreateSnapshot 은 스냅샷 생성을 **발행만** 한다 — 완료를 기다리지 않는다(wait=false).
//
// 큰 컬렉션의 스냅샷은 수 분이 걸려 동기 호출이 HTTP 타임아웃에 걸린다(qdrant 문서가
// 명시적으로 경고하는 지점이다). 그래서 이 계층은 이동/복제와 같은 규율을 따른다 —
// 발행하고, 완료는 ListSnapshots 관측으로 판정한다. 새 스냅샷의 이름도 그 관측에서 온다
// (wait=false 응답에는 이름이 없다).
func (c *HTTPClient) CreateSnapshot(ctx context.Context, collection string) error {
	path := "/collections/" + url.PathEscape(collection) + "/snapshots?wait=false"
	return c.doJSON(ctx, "POST", path, nil, nil)
}

// ListSnapshots 는 이 peer 가 보관 중인 컬렉션 스냅샷 목록이다.
func (c *HTTPClient) ListSnapshots(ctx context.Context, collection string) ([]SnapshotInfo, error) {
	var out []SnapshotInfo
	err := c.doJSON(ctx, "GET", "/collections/"+url.PathEscape(collection)+"/snapshots", nil, &out)
	return out, err
}

// DeleteSnapshot 은 스냅샷 하나를 지운다(보존기간 정리용).
func (c *HTTPClient) DeleteSnapshot(ctx context.Context, collection, name string) error {
	path := "/collections/" + url.PathEscape(collection) + "/snapshots/" + url.PathEscape(name)
	return c.doJSON(ctx, "DELETE", path, nil, nil)
}
