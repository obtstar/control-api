// Package authn 多真人认证与角色（个人版的最小多用户模型）
// 角色：customer（用户/验收）、designer（设计）、tester（测试签发）、
//
//	team（合并评审）、admin（平台管理）
//
// 审批按角色路由：approval.role 仅匹配角色的用户可裁决
package authn

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"golang.org/x/crypto/bcrypt"

	"control-api/internal/store"
)

type Role string

const (
	Customer Role = "customer"
	Designer Role = "designer"
	Tester   Role = "tester"
	Team     Role = "team"
	Admin    Role = "admin"
)

func ValidRole(r string) bool {
	switch Role(r) {
	case Customer, Designer, Tester, Team, Admin:
		return true
	}
	return false
}

type User struct {
	Username string `json:"username"`
	Role     string `json:"role"`
}

type Auth struct {
	St *store.Store
}

// CreateUser 创建账号（仅 admin 操作）
func (a *Auth) CreateUser(username, password, role string) error {
	if !ValidRole(role) {
		return fmt.Errorf("非法角色: %s（customer/designer/tester/team/admin）", role)
	}
	if len(password) < 8 {
		return fmt.Errorf("密码至少 8 位")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	return a.St.AddUser(username, string(hash), role)
}

// LoginWithUser 校验口令，返回用户与会话 token（24h）
func (a *Auth) LoginWithUser(username, password string) (*User, string, error) {
	hash, role, err := a.St.GetUser(username)
	if err != nil {
		return nil, "", fmt.Errorf("用户不存在")
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) != nil {
		return nil, "", fmt.Errorf("密码错误")
	}
	tok := make([]byte, 32)
	rand.Read(tok)
	token := hex.EncodeToString(tok)
	exp := time.Now().Add(24 * time.Hour)
	if err := a.St.AddSession(token, username, role, exp); err != nil {
		return nil, "", err
	}
	return &User{Username: username, Role: role}, token, nil
}

// Login 校验口令，返回会话 token（24h；保持旧接口兼容）
func (a *Auth) Login(username, password string) (string, error) {
	_, token, err := a.LoginWithUser(username, password)
	return token, err
}

// Authenticate token → 用户（中间件用）；按 token 主键查库并校验过期时间
func (a *Auth) Authenticate(token string) (*User, error) {
	username, role, exp, err := a.St.GetSession(token)
	if err != nil {
		return nil, fmt.Errorf("无效会话: %w", err)
	}
	if time.Now().After(exp) {
		return nil, fmt.Errorf("会话已过期")
	}
	return &User{Username: username, Role: role}, nil
}

// CanDecide 角色路由：approval.role 与用户角色匹配（admin 通吃）
func CanDecide(u *User, approvalRole string) bool {
	if u.Role == string(Admin) {
		return true
	}
	return u.Role == approvalRole
}
