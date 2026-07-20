package qdrant

import (
	"context"
	"encoding/json"
	"net/http"
)

// ── B-5 데이터 복사 표면 — scroll 로 읽어 shadow 에 upsert 재삽입 ──
// point 는 무해석 파이프(json.RawMessage): id/vector/payload 를 그대로 옮겨
// qdrant 가 새 shard 수로 재해시 분산하게 한다.

// ScrollPoints 는 offset(next_page_offset 그대로, 첫 페이지는 nil)부터 limit 개를 읽는다.
// 반환 next 가 nil 이면 마지막 페이지.
func (c *HTTPClient) ScrollPoints(ctx context.Context, collection string, offset json.RawMessage, limit int) (points []json.RawMessage, next json.RawMessage, err error) {
	body := map[string]any{"limit": limit, "with_payload": true, "with_vector": true}
	if len(offset) > 0 {
		body["offset"] = offset
	}
	var r struct {
		Points         []json.RawMessage `json:"points"`
		NextPageOffset json.RawMessage   `json:"next_page_offset"`
	}
	if err := c.doJSON(ctx, http.MethodPost, "/collections/"+collection+"/points/scroll", body, &r); err != nil {
		return nil, nil, err
	}
	next = r.NextPageOffset
	if string(next) == "null" {
		next = nil
	}
	return r.Points, next, nil
}

// UpsertPoints 는 points 를 wait=true(내구성 확인)로 재삽입한다 — point id 기준 멱등이라
// 재시도/재개에 안전하다.
func (c *HTTPClient) UpsertPoints(ctx context.Context, collection string, points []json.RawMessage) error {
	return c.doJSON(ctx, http.MethodPut, "/collections/"+collection+"/points?wait=true",
		map[string]any{"points": points}, nil)
}

// ListAliases 는 전역 alias → collection 매핑을 반환한다.
func (c *HTTPClient) ListAliases(ctx context.Context) (map[string]string, error) {
	var r struct {
		Aliases []struct {
			AliasName      string `json:"alias_name"`
			CollectionName string `json:"collection_name"`
		} `json:"aliases"`
	}
	if err := c.doJSON(ctx, http.MethodGet, "/aliases", nil, &r); err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, a := range r.Aliases {
		out[a.AliasName] = a.CollectionName
	}
	return out, nil
}
