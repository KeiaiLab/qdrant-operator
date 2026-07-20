package qdrant

import (
	"context"
	"fmt"
	"sync"
)

// Fake 는 envtest/단위 테스트용 인메모리 구현. 동시 reconcile 에 안전하도록 잠근다.
type Fake struct {
	mu          sync.Mutex
	Collections map[string]CollectionInfo
	// ErrOn 은 메서드명 → 에러 주입 ("GetCollection" 등). 실패 경로 테스트용.
	ErrOn map[string]error
	// Created / Deleted 는 호출 기록 (assert 용).
	Created []string
	Deleted []string
}

func NewFake() *Fake {
	return &Fake{Collections: map[string]CollectionInfo{}, ErrOn: map[string]error{}}
}

func (f *Fake) GetCollection(_ context.Context, name string) (CollectionInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.ErrOn["GetCollection"]; err != nil {
		return CollectionInfo{}, err
	}
	info, ok := f.Collections[name]
	if !ok {
		return CollectionInfo{Exists: false}, nil
	}
	return info, nil
}

func (f *Fake) CreateCollection(_ context.Context, name string, spec CollectionSpec) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.ErrOn["CreateCollection"]; err != nil {
		return err
	}
	if info, ok := f.Collections[name]; ok && info.Exists {
		return fmt.Errorf("collection %s already exists", name)
	}
	f.Collections[name] = CollectionInfo{
		Exists:            true,
		VectorSize:        spec.VectorSize,
		Distance:          spec.Distance,
		ShardNumber:       spec.ShardNumber,
		ReplicationFactor: spec.ReplicationFactor,
	}
	f.Created = append(f.Created, name)
	return nil
}

func (f *Fake) DeleteCollection(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.ErrOn["DeleteCollection"]; err != nil {
		return err
	}
	delete(f.Collections, name)
	f.Deleted = append(f.Deleted, name)
	return nil
}

var _ Client = (*Fake)(nil)
var _ Client = (*HTTPClient)(nil)
