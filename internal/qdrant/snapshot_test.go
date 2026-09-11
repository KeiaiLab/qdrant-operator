/*
Copyright 2026 Keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package qdrant

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// 발행은 완료를 기다리지 않는다 — wait=false 가 붙어야 큰 컬렉션에서 타임아웃하지 않는다.
func TestCreateSnapshot_비동기발행(t *testing.T) {
	var gotQuery, gotMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotQuery = r.Method, r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"result":null,"status":"ok"}`))
	}))
	defer srv.Close()

	if err := NewHTTPClient(srv.URL).CreateSnapshot(context.Background(), "vec"); err != nil {
		t.Fatal(err)
	}
	if gotMethod != "POST" || gotQuery != "wait=false" {
		t.Fatalf("발행 요청: %s ?%s", gotMethod, gotQuery)
	}
}

func TestListSnapshots_파싱(t *testing.T) {
	srv := newTestServer(t, "/collections/vec/snapshots",
		`{"result":[{"name":"a.snapshot","creation_time":"2026-09-09T01:00:00","size":1},{"name":"b.snapshot","size":2}],"status":"ok"}`, nil)
	defer srv.Close()

	got, err := NewHTTPClient(srv.URL).ListSnapshots(context.Background(), "vec")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Name != "a.snapshot" || got[1].Size != 2 {
		t.Fatalf("목록 파싱: %+v", got)
	}
	// creation_time 은 qdrant 가 비워 보낼 수 있다 — 그때도 실패하지 않아야 한다.
	if got[1].CreationTime != "" {
		t.Fatalf("빈 creation_time 처리: %+v", got[1])
	}
}

// 컬렉션·스냅샷 이름이 경로에 그대로 들어가므로 이스케이프가 필요하다. 이름을 만드는 것은
// qdrant 지만 컬렉션 이름은 사용자가 정한다.
func TestDeleteSnapshot_경로이스케이프(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.EscapedPath()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"result":true,"status":"ok"}`))
	}))
	defer srv.Close()

	if err := NewHTTPClient(srv.URL).DeleteSnapshot(context.Background(), "my vec", "a b.snapshot"); err != nil {
		t.Fatal(err)
	}
	if want := "/collections/my%20vec/snapshots/a%20b.snapshot"; gotPath != want {
		t.Fatalf("경로=%s want=%s", gotPath, want)
	}
}

func TestFakeSnapshot_왕복(t *testing.T) {
	f := NewFake()
	f.SetCollection("vec", CollectionInfo{Exists: true})
	ctx := context.Background()

	if err := f.CreateSnapshot(ctx, "vec"); err != nil {
		t.Fatal(err)
	}
	if err := f.CreateSnapshot(ctx, "vec"); err != nil {
		t.Fatal(err)
	}

	// 이름은 발행 응답이 아니라 관측에서 온다(wait=false 응답에는 이름이 없다).
	list, _ := f.ListSnapshots(ctx, "vec")
	if len(list) != 2 || list[0].Name == list[1].Name {
		t.Fatalf("목록: %+v", list)
	}
	first, second := list[0], list[1]

	if err := f.DeleteSnapshot(ctx, "vec", first.Name); err != nil {
		t.Fatal(err)
	}
	if list, _ = f.ListSnapshots(ctx, "vec"); len(list) != 1 || list[0].Name != second.Name {
		t.Fatalf("삭제 후: %+v", list)
	}

	// 없는 스냅샷 삭제는 조용히 성공하면 안 된다 — 보존기간 정리가 헛돌게 된다.
	if err := f.DeleteSnapshot(ctx, "vec", "없음"); err == nil {
		t.Fatal("없는 스냅샷 삭제가 성공했다")
	}
	// 없는 컬렉션 스냅샷도 마찬가지.
	if err := f.CreateSnapshot(ctx, "없음"); err == nil {
		t.Fatal("없는 컬렉션 스냅샷이 성공했다")
	}
}

// qdrant 는 wait=false 발행에 **202 Accepted** 로 답한다 — 수락이지 실패가 아니다.
// 실측(2026-09-11 00:55Z 라이브): 202 를 실패로 판정해 QdrantBackup 이
// Degraded(SnapshotFailed) 로 떨어졌고 백업이 한 번도 성공하지 못했다.
func TestCreateSnapshot_202수락(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"status":"accepted","result":null}`))
	}))
	defer srv.Close()

	if err := NewHTTPClient(srv.URL).CreateSnapshot(context.Background(), "vec"); err != nil {
		t.Fatalf("202 Accepted 를 실패로 판정: %v", err)
	}
}

// 넓힌 것은 2xx 까지지 판정 자체가 아니다 — 오류 응답은 여전히 실패여야 한다.
func TestCreateSnapshot_오류응답(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"status":{"error":"disk full"}}`))
	}))
	defer srv.Close()

	if err := NewHTTPClient(srv.URL).CreateSnapshot(context.Background(), "vec"); err == nil {
		t.Fatal("500 응답을 성공으로 판정")
	}
}
