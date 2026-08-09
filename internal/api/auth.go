// 认证端点与中间件
package api

import (
	"encoding/json"
	"net/http"
	"strings"
)

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

// withAuth 鉴权中间件：/actuator/health 与 /api/auth/login 放行
func (s *server) withAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if p == "/actuator/health" || p == "/api/auth/login" {
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
		r.Header.Set("X-User", u.Username)
		r.Header.Set("X-Role", u.Role)
		next.ServeHTTP(w, r)
	})
}

type errString string

func (e errString) Error() string { return string(e) }
