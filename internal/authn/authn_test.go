// authn 包单测（TASK-000015）：表驱动 + :memory: SQLite 实测，不 mock DB。
package authn

import (
	"strings"
	"testing"

	"control-api/internal/store"
)

// newTestAuth :memory: store + Auth 实例（engine_test 同范式）
func newTestAuth(t *testing.T) *Auth {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	st.SetMaxOpenConns(1) // :memory: 每连接一个库，限单连接保证一致
	t.Cleanup(func() { st.Close() })
	return &Auth{St: st}
}

func TestValidRole(t *testing.T) {
	cases := []struct {
		role string
		want bool
	}{
		{"customer", true},
		{"designer", true},
		{"tester", true},
		{"team", true},
		{"admin", true},
		{"root", false},
		{"", false},
		{"ADMIN", false},
	}
	for _, c := range cases {
		if got := ValidRole(c.role); got != c.want {
			t.Errorf("ValidRole(%q) = %v, want %v", c.role, got, c.want)
		}
	}
}

func TestCanDecide(t *testing.T) {
	u := func(role string) *User { return &User{Username: "u", Role: role} }
	cases := []struct {
		name string
		user *User
		appr string
		want bool
	}{
		{"admin 通吃 customer", u("admin"), "customer", true},
		{"admin 通吃 team", u("admin"), "team", true},
		{"同角色 designer", u("designer"), "designer", true},
		{"异角色 designer/customer", u("designer"), "customer", false},
		{"空审批角色仅 admin 可批", u("team"), "", false},
		{"空用户角色", u(""), "team", false},
	}
	for _, c := range cases {
		if got := CanDecide(c.user, c.appr); got != c.want {
			t.Errorf("%s: CanDecide(%v,%q) = %v, want %v", c.name, c.user, c.appr, got, c.want)
		}
	}
}

func TestCreateUserAndLogin(t *testing.T) {
	a := newTestAuth(t)
	if err := a.CreateUser("alice", "password123", "designer"); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	// 正确口令
	u, token, err := a.LoginWithUser("alice", "password123")
	if err != nil || u.Role != "designer" || token == "" {
		t.Fatalf("LoginWithUser 正确口令: u=%v token=%q err=%v", u, token, err)
	}
	// 错误口令
	if _, _, err := a.LoginWithUser("alice", "wrongpass"); err == nil {
		t.Error("错误口令应报错")
	}
	// 非法角色/短密码
	if err := a.CreateUser("bob", "password123", "root"); err == nil {
		t.Error("非法角色应报错")
	}
	if err := a.CreateUser("bob", "short", "admin"); err == nil {
		t.Error("短密码应报错")
	}
}

func TestAuthenticate(t *testing.T) {
	a := newTestAuth(t)
	if err := a.CreateUser("carol", "password123", "team"); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	_, token, err := a.LoginWithUser("carol", "password123")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if u, err := a.Authenticate(token); err != nil || u.Username != "carol" {
		t.Errorf("有效 token: u=%v err=%v", u, err)
	}
	if _, err := a.Authenticate("forged-token"); err == nil || !strings.Contains(err.Error(), "会话") {
		t.Errorf("伪造 token 应报会话错误, got %v", err)
	}
}
