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

func TestCreateSnapshot_파싱(t *testing.T) {
	srv := newTestServer(t, "/collections/vec/snapshots",
		`{"result":{"name":"vec-2026-09-10-01-00-00.snapshot","creation_time":"2026-09-10T01:00:00","size":4096},"status":"ok"}`, nil)
	defer srv.Close()

	got, err := NewHTTPClient(srv.URL).CreateSnapshot(context.Background(), "vec")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "vec-2026-09-10-01-00-00.snapshot" || got.Size != 4096 {
		t.Fatalf("스냅샷 파싱: %+v", got)
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

	first, err := f.CreateSnapshot(ctx, "vec")
	if err != nil {
		t.Fatal(err)
	}
	second, _ := f.CreateSnapshot(ctx, "vec")
	if first.Name == second.Name {
		t.Fatalf("이름이 겹침: %s", first.Name)
	}

	list, _ := f.ListSnapshots(ctx, "vec")
	if len(list) != 2 {
		t.Fatalf("목록 %d건", len(list))
	}

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
	if _, err := f.CreateSnapshot(ctx, "없음"); err == nil {
		t.Fatal("없는 컬렉션 스냅샷이 성공했다")
	}
}
