// RESTSearcher 表驱动测试：httptest fake server 覆盖命中/空结果/非 200/
// 坏 JSON/超时，及 api_key 认证头。不依赖真实 PieKBS 进程。
package kb

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// newFakeKB 按给定的 handler 起 httptest server 并返回指向它的 RESTSearcher
func newFakeKB(t *testing.T, h http.HandlerFunc) (*RESTSearcher, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return &RESTSearcher{Endpoint: srv.URL}, srv
}

func TestRESTSearcherSearch(t *testing.T) {
	cases := []struct {
		name    string
		handler http.HandlerFunc
		wantN   int    // 期望命中条数（-1 表示期望报错）
		wantErr string // 期望错误子串
	}{
		{"正常命中", func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("q") == "" {
				t.Error("缺少 q 参数")
			}
			if r.URL.Query().Get("limit") != "3" {
				t.Errorf("limit = %q, want 3", r.URL.Query().Get("limit"))
			}
			writeJSON(t, w, map[string]any{"results": []Hit{
				{ID: "wiki/a.md", Path: "wiki/a.md", Title: "A", Layer: "L2"},
				{ID: "wiki/b.md", Path: "wiki/b.md", Title: "B", Layer: "L3"},
			}, "conflicts": []any{}})
		}, 2, ""},
		{"空结果", func(w http.ResponseWriter, r *http.Request) {
			writeJSON(t, w, map[string]any{"results": []Hit{}, "conflicts": []any{}})
		}, 0, ""},
		{"非 200", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			writeJSON(t, w, map[string]string{"error": "boom"})
		}, -1, "HTTP 500"},
		{"坏 JSON", func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte("{not json"))
		}, -1, "解析响应"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, _ := newFakeKB(t, c.handler)
			hits, err := s.Search(context.Background(), "测试", 3)
			if c.wantN < 0 {
				if err == nil {
					t.Fatal("应报错")
				}
				if c.wantErr != "" && !strings.Contains(err.Error(), c.wantErr) {
					t.Fatalf("err = %v, 期望包含 %q", err, c.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(hits) != c.wantN {
				t.Fatalf("命中 %d 条, want %d", len(hits), c.wantN)
			}
			if c.wantN > 0 && (hits[0].Title == "" || hits[0].Path == "") {
				t.Fatalf("Hit 字段未对齐: %+v", hits[0])
			}
		})
	}
}

// api_key 非空时带 Authorization: Bearer 头（对齐 piekbs withAuth 优先头）
func TestRESTSearcherSendsBearer(t *testing.T) {
	s, _ := newFakeKB(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer secret" {
			t.Errorf("Authorization = %q, want Bearer secret", got)
		}
		writeJSON(t, w, map[string]any{"results": []Hit{}})
	})
	s.APIKey = "secret"
	if _, err := s.Search(context.Background(), "q", 1); err != nil {
		t.Fatal(err)
	}
}

// 服务端超时应如实报错（客户端 5s 超时，测试用短客户端模拟）
func TestRESTSearcherTimeout(t *testing.T) {
	s, _ := newFakeKB(t, func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		writeJSON(t, w, map[string]any{"results": []Hit{}})
	})
	s.Client = &http.Client{Timeout: 50 * time.Millisecond}
	if _, err := s.Search(context.Background(), "q", 1); err == nil {
		t.Fatal("超时应报错")
	}
}

func writeJSON(t *testing.T, w http.ResponseWriter, v any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		t.Fatal(err)
	}
}
