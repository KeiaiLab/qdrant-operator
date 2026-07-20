package qdrant

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// HTTPClient 는 Qdrant REST(6333) 실구현. baseURL 예: http://platform-data-qdrant.data.svc:6333
type HTTPClient struct {
	BaseURL string
	HC      *http.Client
}

func NewHTTPClient(baseURL string) *HTTPClient {
	return &HTTPClient{BaseURL: baseURL, HC: &http.Client{Timeout: 15 * time.Second}}
}

// collectionResponse 는 GET /collections/{name} 응답에서 필요한 부분만 디코드한다.
// vectors 는 단일(named 아님) 스키마를 가정 — named vectors 는 후속 확장.
type collectionResponse struct {
	Result struct {
		PointsCount uint64 `json:"points_count"`
		Config      struct {
			Params struct {
				Vectors struct {
					Size     uint64 `json:"size"`
					Distance string `json:"distance"`
				} `json:"vectors"`
				ShardNumber       uint32 `json:"shard_number"`
				ReplicationFactor uint32 `json:"replication_factor"`
			} `json:"params"`
		} `json:"config"`
	} `json:"result"`
	Status string `json:"status"`
}

func (c *HTTPClient) GetCollection(ctx context.Context, name string) (CollectionInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/collections/"+name, nil)
	if err != nil {
		return CollectionInfo{}, err
	}
	resp, err := c.HC.Do(req)
	if err != nil {
		return CollectionInfo{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return CollectionInfo{Exists: false}, nil
	}
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return CollectionInfo{}, fmt.Errorf("GET /collections/%s: %s: %s", name, resp.Status, string(b))
	}
	var cr collectionResponse
	if err := json.NewDecoder(resp.Body).Decode(&cr); err != nil {
		return CollectionInfo{}, err
	}
	p := cr.Result.Config.Params
	return CollectionInfo{
		Exists:            true,
		PointsCount:       cr.Result.PointsCount,
		VectorSize:        p.Vectors.Size,
		Distance:          p.Vectors.Distance,
		ShardNumber:       p.ShardNumber,
		ReplicationFactor: p.ReplicationFactor,
	}, nil
}

func (c *HTTPClient) CreateCollection(ctx context.Context, name string, spec CollectionSpec) error {
	body := map[string]any{
		"vectors":            map[string]any{"size": spec.VectorSize, "distance": spec.Distance},
		"shard_number":       spec.ShardNumber,
		"replication_factor": spec.ReplicationFactor,
	}
	buf, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, c.BaseURL+"/collections/"+name, bytes.NewReader(buf))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HC.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("PUT /collections/%s: %s: %s", name, resp.Status, string(b))
	}
	return nil
}

func (c *HTTPClient) DeleteCollection(ctx context.Context, name string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.BaseURL+"/collections/"+name, nil)
	if err != nil {
		return err
	}
	resp, err := c.HC.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNotFound {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("DELETE /collections/%s: %s: %s", name, resp.Status, string(b))
	}
	return nil
}
