// Package kb 知识库依据检索窄接口（18.3 grounding，FINDING-016）。
// Searcher 当前由 RESTSearcher（PieKBS REST）实现，将来可换 MCP 实现而业务不动。
package kb

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Hit 一条检索命中，字段对齐 piekbs kb.SearchResult（internal/kb/search.go）
type Hit struct {
	ID      string `json:"id"`
	Path    string `json:"path"`
	Title   string `json:"title"`
	Layer   string `json:"layer"`
	Kind    string `json:"kind"`
	Snippet string `json:"snippet,omitempty"`
}

// Searcher 依据检索窄接口
type Searcher interface {
	Search(ctx context.Context, query string, limit int) ([]Hit, error)
}

// RESTSearcher PieKBS REST 实现：GET {endpoint}/api/search?q=…&limit=…。
// 响应为 kb.SearchResponse（{results, conflicts}），仅取 results。
type RESTSearcher struct {
	Endpoint string // PieKBS 服务地址，如 http://127.0.0.1:8766
	APIKey   string // 非空时带 Authorization: Bearer 头
	// Client 可选 HTTP 客户端；nil 使用默认 5s 超时客户端
	Client *http.Client
}

// Search 检索 KB 依据；非 200 与 JSON 解析错误如实返回
func (s *RESTSearcher) Search(ctx context.Context, query string, limit int) ([]Hit, error) {
	u := strings.TrimRight(s.Endpoint, "/") + "/api/search?q=" +
		url.QueryEscape(query) + "&limit=" + strconv.Itoa(limit)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("kb search 构造请求: %w", err)
	}
	if s.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+s.APIKey)
	}
	client := s.Client
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("kb search 请求 %s: %w", s.Endpoint, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("kb search HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var out struct {
		Results []Hit `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("kb search 解析响应: %w", err)
	}
	return out.Results, nil
}
