// 认证端点与中间件
package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
)

// 身份经 context 传递（FINDING-026）：不放进可变请求头，绕过 withAuth 的
// 路由拿到空身份而非伪造头。withIdentity 为 withAuth 与测试共用注入入口
type ctxKey string

const (
	ctxUser ctxKey = "user"
	ctxRole ctxKey = "role"
)

func withIdentity(r *http.Request, username, role string) *http.Request {
	ctx := context.WithValue(r.Context(), ctxUser, username)
	ctx = context.WithValue(ctx, ctxRole, role)
	return r.WithContext(ctx)
}

// identity 读取请求身份；未注入（未经 withAuth）返回空串
func identity(r *http.Request) (username, role string) {
	username, _ = r.Context().Value(ctxUser).(string)
	role, _ = r.Context().Value(ctxRole).(string)
	return
}

type loginReq struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func (s *server) login(w http.ResponseWriter, r *http.Request) {
	var req loginReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, err)
		return
	}
	user, token, err := s.auth.LoginWithUser(req.Username, req.Password)
	if err != nil {
		writeErr(w, 401, err)
		return
	}
	writeJSON(w, map[string]any{"token": token, "username": user.Username, "role": user.Role})
}

// withAuth 鉴权中间件：/actuator/health、/api/auth/login 与 /api/webhooks/ 放行
// （webhook 走独立共享密钥认证，在 handler 内自验，不用 Bearer 会话）
func (s *server) withAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if p == "/actuator/health" || p == "/api/auth/login" || strings.HasPrefix(p, "/api/webhooks/") {
			next.ServeHTTP(w, r)
			return
		}
		tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if tok == "" || tok == r.Header.Get("Authorization") {
			writeErr(w, 401, errString("缺少 Bearer token"))
			return
		}
		u, err := s.auth.Authenticate(tok)
		if err != nil {
			writeErr(w, 401, err)
			return
		}
		next.ServeHTTP(w, withIdentity(r, u.Username, u.Role))
	})
}

type errString string

func (e errString) Error() string { return string(e) }
