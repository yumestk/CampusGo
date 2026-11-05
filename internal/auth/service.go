package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/yumestk/CampusGo/internal/platform/fault"
)

type User struct {
	ID       int64  `json:"id,string"`
	CampusID int64  `json:"campusId,string"`
	Name     string `json:"name"`
	Role     string `json:"role"`
}
type Session struct {
	Token        string `json:"token"`
	UserID       int64  `json:"userId,string"`
	ExpiresIn    int64  `json:"expiresIn,string"`
	RefreshToken string `json:"refreshToken,omitempty"`
	Mode         string `json:"mode,omitempty"`
	expiresAt    time.Time
}
type Service struct {
	DB     *sql.DB
	Redis  *redis.Client
	Config Config
}

func randomToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

var tokenPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)

func ValidToken(token string) bool { return tokenPattern.MatchString(token) }
func (s *Service) key(kind, token string) string {
	hash := sha256.Sum256([]byte(token))
	mode := "live"
	if s.Config.Demo {
		mode = "demo"
	}
	return "campusgo:auth:" + s.Config.Environment + ":" + mode + ":" + s.Config.Namespace + ":" + kind + ":" + hex.EncodeToString(hash[:])
}
func (s *Service) User(ctx context.Context, id int64) (User, error) {
	var u User
	var active bool
	err := s.DB.QueryRowContext(ctx, "SELECT id,campus_id,display_name,role,active FROM app_user WHERE id=?", id).Scan(&u.ID, &u.CampusID, &u.Name, &u.Role, &active)
	if errors.Is(err, sql.ErrNoRows) {
		return u, fault.New("UNAUTHORIZED", 401)
	}
	if err != nil {
		return u, fault.Dependency(err)
	}
	if !active {
		return u, fault.New("FORBIDDEN", 403)
	}
	return u, nil
}
func (s *Service) issue(ctx context.Context, id int64) (Session, error) {
	if s.Config.Mode == "jwt" {
		return s.issueJWT(ctx, id)
	}
	for range 3 {
		token := randomToken()
		ok, err := s.Redis.SetNX(ctx, s.key("session", token), strconv.FormatInt(id, 10), s.Config.SessionTTL).Result()
		if err != nil {
			return Session{}, fault.Dependency(err)
		}
		if ok {
			return Session{Token: token, UserID: id, ExpiresIn: int64(s.Config.SessionTTL / time.Second), Mode: "session"}, nil
		}
	}
	return Session{}, fault.Dependency(fmt.Errorf("session token collision budget exhausted"))
}
func (s *Service) DemoLogin(ctx context.Context, id int64) (Session, error) {
	if !s.Config.Demo || (s.Config.Environment != "local" && s.Config.Environment != "test") {
		return Session{}, fault.New("UNAUTHORIZED", 401)
	}
	// Demo is a closed set, never an arbitrary positive user ID backdoor.
	if id != 1001 && id != 2001 && id != 2002 && id != 9001 {
		return Session{}, fault.New("UNAUTHORIZED", 401)
	}
	if _, err := s.User(ctx, id); err != nil {
		return Session{}, err
	}
	return s.issue(ctx, id)
}
func (s *Service) Resolve(ctx context.Context, token string) (User, error) {
	if s.Config.Mode == "jwt" {
		return s.resolveJWT(ctx, token)
	}
	if !ValidToken(token) {
		return User{}, fault.New("UNAUTHORIZED", 401)
	}
	value, err := s.Redis.Get(ctx, s.key("session", token)).Result()
	if errors.Is(err, redis.Nil) {
		return User{}, fault.New("UNAUTHORIZED", 401)
	}
	if err != nil {
		return User{}, fault.Dependency(err)
	}
	id, err := strconv.ParseInt(value, 10, 64)
	if err != nil || id <= 0 {
		return User{}, fault.Dependency(fmt.Errorf("corrupt session user ID"))
	}
	return s.User(ctx, id)
}
func (s *Service) Logout(ctx context.Context, token string) error {
	if s.Config.Mode == "jwt" {
		return s.logoutJWT(ctx, token)
	}
	if !ValidToken(token) {
		return fault.New("UNAUTHORIZED", 401)
	}
	return fault.Dependency(s.Redis.Del(ctx, s.key("session", token)).Err())
}
func (s *Service) verifiedLogin(ctx context.Context, issuer, subject string) (Session, error) {
	var id int64
	err := s.DB.QueryRowContext(ctx, "SELECT user_id FROM identity_binding WHERE issuer=? AND subject=?", issuer, subject).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, fault.New("IDENTITY_NOT_BOUND", 403)
	}
	if err != nil {
		return Session{}, fault.Dependency(err)
	}
	if _, err := s.User(ctx, id); err != nil {
		return Session{}, err
	}
	return s.issue(ctx, id)
}
