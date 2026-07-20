package qdrant

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"slices"
	"sync"
)

// Fake 는 envtest/단위 테스트용 인메모리 구현. 동시 reconcile 에 안전하도록 잠근다.
// 분포 시뮬레이션: Peers/Placement 를 테스트가 직접 세팅하고, MoveShard 는 즉시 배치를
// 옮긴다(전송 지연은 InFlight 를 테스트가 넣었다 빼며 폴링 경로를 검증).
type Fake struct {
	mu          sync.Mutex
	Collections map[string]CollectionInfo
	// ErrOn 은 메서드명 → 에러 주입 ("GetCollection" 등). 실패 경로 테스트용.
	ErrOn map[string]error
	// Created / Deleted 는 호출 기록 (assert 용).
	Created []string
	Deleted []string

	// ── 분포 시뮬레이션 (B-2~B-5) ──
	SelfPeerID uint64
	PeersList  []Peer
	// Placement: 컬렉션 → shardID → peerID. CreateCollection 이 라운드로빈으로 초기화.
	Placement map[string]map[uint32]uint64
	// InFlight: 컬렉션 → 진행 중 전송(테스트가 직접 제어 — CollectionCluster 가 노출).
	InFlight map[string][]TransferInfo
	// 기록
	Moves        []string // "coll/shard:from->to"
	RemovedPeers []uint64
	Aliases      map[string]string // alias → collection
	AliasLog     [][]AliasAction
}

func NewFake() *Fake {
	return &Fake{
		Collections: map[string]CollectionInfo{},
		ErrOn:       map[string]error{},
		SelfPeerID:  1,
		PeersList:   []Peer{{ID: 1, URI: "http://fake-0.fake-headless:6335/"}},
		Placement:   map[string]map[uint32]uint64{},
		InFlight:    map[string][]TransferInfo{},
		Aliases:     map[string]string{},
	}
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
	// shard 를 peer 에 라운드로빈 배치 (qdrant 의 생성 시 분산 배치 모사).
	pl := map[uint32]uint64{}
	for i := uint32(0); i < spec.ShardNumber; i++ {
		pl[i] = f.PeersList[int(i)%len(f.PeersList)].ID
	}
	f.Placement[name] = pl
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
	delete(f.Placement, name)
	f.Deleted = append(f.Deleted, name)
	return nil
}

func (f *Fake) ListCollections(_ context.Context) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.ErrOn["ListCollections"]; err != nil {
		return nil, err
	}
	names := make([]string, 0, len(f.Collections))
	for n, info := range f.Collections {
		if info.Exists {
			names = append(names, n)
		}
	}
	slices.Sort(names)
	return names, nil
}

func (f *Fake) ClusterInfo(_ context.Context) (*ClusterInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.ErrOn["ClusterInfo"]; err != nil {
		return nil, err
	}
	peers := append([]Peer(nil), f.PeersList...)
	slices.SortFunc(peers, func(a, b Peer) int { return cmp.Compare(a.ID, b.ID) })
	return &ClusterInfo{Enabled: true, PeerID: f.SelfPeerID, Peers: peers, RaftLeader: f.SelfPeerID}, nil
}

func (f *Fake) CollectionCluster(_ context.Context, name string) (*CollectionClusterInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.ErrOn["CollectionCluster"]; err != nil {
		return nil, err
	}
	pl, ok := f.Placement[name]
	if !ok {
		return nil, fmt.Errorf("collection %s not found", name)
	}
	cc := &CollectionClusterInfo{PeerID: f.SelfPeerID, ShardCount: len(pl)}
	ids := make([]uint32, 0, len(pl))
	for sid := range pl {
		ids = append(ids, sid)
	}
	slices.Sort(ids)
	for _, sid := range ids {
		cc.Shards = append(cc.Shards, ShardInfo{ShardID: sid, PeerID: pl[sid], State: "Active"})
	}
	cc.Transfers = append(cc.Transfers, f.InFlight[name]...)
	return cc, nil
}

func (f *Fake) MoveShard(_ context.Context, collection string, shardID uint32, from, to uint64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.ErrOn["MoveShard"]; err != nil {
		return err
	}
	pl, ok := f.Placement[collection]
	if !ok {
		return fmt.Errorf("collection %s not found", collection)
	}
	if pl[shardID] != from {
		return fmt.Errorf("shard %d 는 peer %d 에 없음(실제 %d)", shardID, from, pl[shardID])
	}
	pl[shardID] = to
	f.Moves = append(f.Moves, fmt.Sprintf("%s/%d:%d->%d", collection, shardID, from, to))
	return nil
}

func (f *Fake) RemovePeer(_ context.Context, peerID uint64, force bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.ErrOn["RemovePeer"]; err != nil {
		return err
	}
	if !force {
		// 실제 qdrant 시맨틱 모사: shard 가 남은 peer 는 제거 거부.
		for coll, pl := range f.Placement {
			for sid, pid := range pl {
				if pid == peerID {
					return fmt.Errorf("peer %d 에 shard 잔존(%s/%d) — 제거 거부", peerID, coll, sid)
				}
			}
		}
	}
	kept := f.PeersList[:0]
	for _, p := range f.PeersList {
		if p.ID != peerID {
			kept = append(kept, p)
		}
	}
	f.PeersList = kept
	f.RemovedPeers = append(f.RemovedPeers, peerID)
	return nil
}

func (f *Fake) UpdateAliases(_ context.Context, actions []AliasAction) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.ErrOn["UpdateAliases"]; err != nil {
		return err
	}
	for _, a := range actions {
		if a.DeleteAlias != nil {
			delete(f.Aliases, a.DeleteAlias.AliasName)
		}
		if a.CreateAlias != nil {
			f.Aliases[a.CreateAlias.AliasName] = a.CreateAlias.CollectionName
		}
	}
	f.AliasLog = append(f.AliasLog, actions)
	return nil
}

// ── 테스트용 동시성 안전 세터 (러닝 매니저와의 동시 접근 레이스 방지) ──

// SetPeers 는 클러스터 peer 목록을 교체한다(첫 peer 가 self).
func (f *Fake) SetPeers(peers ...Peer) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.PeersList = append([]Peer(nil), peers...)
	if len(peers) > 0 {
		f.SelfPeerID = peers[0].ID
	}
}

// SetCollection 은 컬렉션 메타를 직접 세팅한다(기존 컬렉션 채택 시나리오용).
func (f *Fake) SetCollection(name string, info CollectionInfo) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Collections[name] = info
}

// SetPlacement 는 컬렉션의 shard→peer 배치를 직접 세팅한다(분포 시나리오 주입용).
func (f *Fake) SetPlacement(name string, pl map[uint32]uint64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := map[uint32]uint64{}
	maps.Copy(cp, pl)
	f.Placement[name] = cp
}

// Snapshot 헬퍼 — 검증용 읽기(복사 반환).
func (f *Fake) GetPlacement(name string) map[uint32]uint64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := map[uint32]uint64{}
	maps.Copy(cp, f.Placement[name])
	return cp
}

var _ Client = (*Fake)(nil)
var _ Client = (*HTTPClient)(nil)
