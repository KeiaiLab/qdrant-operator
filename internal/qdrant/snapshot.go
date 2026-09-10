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

// CreateSnapshot 은 이 peer 가 가진 컬렉션 shard 의 스냅샷을 만든다.
func (c *HTTPClient) CreateSnapshot(ctx context.Context, collection string) (SnapshotInfo, error) {
	var out SnapshotInfo
	err := c.doJSON(ctx, "POST", "/collections/"+url.PathEscape(collection)+"/snapshots", nil, &out)
	return out, err
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
