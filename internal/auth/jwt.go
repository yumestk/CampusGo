package auth

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/yumestk/CampusGo/internal/platform/fault"
)

type accessClaims struct {
	jwt.Claims
	Family string `json:"sid"`
}

func (s *Service) jwtIssuer() string          { return "campusgo:" + s.key("issuer", "v1") }
func (s *Service) familyKey(id string) string { return s.key("jwt-family", id) }
func digest(v string) string                  { h := sha256.Sum256([]byte(v)); return hex.EncodeToString(h[:]) }
func (s *Service) jwtSession(id int64, family, refresh string) (Session, error) {
	now := time.Now().Truncate(time.Second)
	c := accessClaims{Claims: jwt.Claims{Issuer: s.jwtIssuer(), Subject: strconv.FormatInt(id, 10), Audience: jwt.Audience{"campusgo-api"}, ID: randomToken(), IssuedAt: jwt.NewNumericDate(now), NotBefore: jwt.NewNumericDate(now), Expiry: jwt.NewNumericDate(now.Add(s.Config.SessionTTL))}, Family: family}
	signer, e := jose.NewSigner(jose.SigningKey{Algorithm: jose.HS256, Key: []byte(s.Config.JWTSecret)}, (&jose.SignerOptions{}).WithType("JWT"))
	if e != nil {
		return Session{}, e
	}
	raw, e := jwt.Signed(signer).Claims(c).Serialize()
	return Session{Token: raw, UserID: id, ExpiresIn: int64(s.Config.SessionTTL / time.Second), RefreshToken: family + "." + refresh, Mode: "jwt", expiresAt: now.Add(s.Config.SessionTTL)}, e
}
func (s *Service) parseJWT(raw string, expired bool) (accessClaims, error) {
	var c accessClaims
	if len(raw) > 4096 {
		return c, fault.New("UNAUTHORIZED", 401)
	}
	t, e := jwt.ParseSigned(raw, []jose.SignatureAlgorithm{jose.HS256})
	if e != nil {
		return c, fault.New("UNAUTHORIZED", 401)
	}
	if e = t.Claims([]byte(s.Config.JWTSecret), &c); e != nil {
		return c, fault.New("UNAUTHORIZED", 401)
	}
	if c.Expiry == nil || c.IssuedAt == nil || c.NotBefore == nil || !ValidToken(c.ID) || !ValidToken(c.Family) || len(c.Audience) != 1 {
		return c, fault.New("UNAUTHORIZED", 401)
	}
	check := c.Claims
	if expired {
		check.Expiry = jwt.NewNumericDate(time.Now().Add(time.Second))
	}
	if e = check.ValidateWithLeeway(jwt.Expected{Issuer: s.jwtIssuer(), AnyAudience: jwt.Audience{"campusgo-api"}, Time: time.Now()}, 0); e != nil {
		return c, fault.New("UNAUTHORIZED", 401)
	}
	id, e := strconv.ParseInt(c.Subject, 10, 64)
	if e != nil || id <= 0 || strconv.FormatInt(id, 10) != c.Subject || c.Expiry.Time().Before(c.IssuedAt.Time()) || c.Expiry.Time().Sub(c.IssuedAt.Time()) > 24*time.Hour {
		return c, fault.New("UNAUTHORIZED", 401)
	}
	return c, nil
}
func (s *Service) issueJWT(ctx context.Context, id int64) (Session, error) {
	family, secret := randomToken(), randomToken()
	out, e := s.jwtSession(id, family, secret)
	if e != nil {
		return Session{}, fault.Dependency(e)
	}
	ttl := s.Config.RefreshTTL
	if ttl == 0 {
		ttl = 7 * 24 * time.Hour
	}
	// A single key keeps issuance, rotation, replay receipt and family revocation atomic.
	n, e := s.Redis.Eval(ctx, `if redis.call('EXISTS',KEYS[1])==1 then return 0 end
 redis.call('HSET',KEYS[1],'uid',ARGV[1],'current',ARGV[2],'revoked','0','generation','0')
 redis.call('PEXPIRE',KEYS[1],ARGV[3]);return 1`, []string{s.familyKey(family)}, strconv.FormatInt(id, 10), digest(secret), ttl.Milliseconds()).Int64()
	if e != nil {
		return Session{}, fault.Dependency(e)
	}
	if n != 1 {
		return Session{}, fault.Dependency(fmt.Errorf("JWT family collision"))
	}
	return out, nil
}
func (s *Service) resolveJWT(ctx context.Context, raw string) (User, error) {
	c, e := s.parseJWT(raw, false)
	if e != nil {
		return User{}, e
	}
	v, e := s.Redis.HMGet(ctx, s.familyKey(c.Family), "uid", "revoked").Result()
	if e != nil {
		return User{}, fault.Dependency(e)
	}
	if v[0] != c.Subject || v[1] != "0" {
		return User{}, fault.New("UNAUTHORIZED", 401)
	}
	id, _ := strconv.ParseInt(c.Subject, 10, 64)
	return s.User(ctx, id)
}
func (s *Service) logoutJWT(ctx context.Context, raw string) error {
	c, e := s.parseJWT(raw, true)
	if e != nil {
		return e
	}
	_, e = s.Redis.Eval(ctx, `if redis.call('HGET',KEYS[1],'uid')==ARGV[1] then redis.call('HSET',KEYS[1],'revoked','1'); redis.call('HDEL',KEYS[1],'receipt') end;return 1`, []string{s.familyKey(c.Family)}, c.Subject).Result()
	return fault.Dependency(e)
}
func (s *Service) receiptCipher() (cipher.AEAD, error) {
	h := sha256.Sum256([]byte("campusgo-refresh-receipt:" + s.Config.JWTSecret))
	b, e := aes.NewCipher(h[:])
	if e != nil {
		return nil, e
	}
	return cipher.NewGCM(b)
}
func (s *Service) seal(out Session, aad string) (string, error) {
	a, e := s.receiptCipher()
	if e != nil {
		return "", e
	}
	b, e := json.Marshal(out)
	if e != nil {
		return "", e
	}
	n := make([]byte, a.NonceSize())
	if _, e = rand.Read(n); e != nil {
		return "", e
	}
	return base64.RawURLEncoding.EncodeToString(a.Seal(n, n, b, []byte(aad))), nil
}
func (s *Service) unseal(raw, aad string) (Session, error) {
	var out Session
	a, e := s.receiptCipher()
	if e != nil {
		return out, e
	}
	b, e := base64.RawURLEncoding.DecodeString(raw)
	if e != nil || len(b) < a.NonceSize() {
		return out, fmt.Errorf("invalid refresh receipt")
	}
	b, e = a.Open(nil, b[:a.NonceSize()], b[a.NonceSize():], []byte(aad))
	if e != nil {
		return out, e
	}
	e = json.Unmarshal(b, &out)
	return out, e
}

var rotateJWT = redis.NewScript(`
 local uid=redis.call('HGET',KEYS[1],'uid')
 if not uid or redis.call('HGET',KEYS[1],'revoked')~='0' then return {'REJECT'} end
 local now=redis.call('TIME'); local ms=now[1]*1000+math.floor(now[2]/1000)
 if redis.call('HGET',KEYS[1],'previous')==ARGV[1] and redis.call('HGET',KEYS[1],'request')==ARGV[2] and tonumber(redis.call('HGET',KEYS[1],'receipt_until') or '0')>ms then return {'REPLAY',redis.call('HGET',KEYS[1],'receipt')} end
 if redis.call('HGET',KEYS[1],'current')~=ARGV[1] then
  if redis.call('HEXISTS',KEYS[1],'used:'..ARGV[1])==1 then redis.call('HSET',KEYS[1],'revoked','1');redis.call('HDEL',KEYS[1],'receipt');return {'REUSE'} end
  return {'REJECT'}
 end
 if uid~=ARGV[5] then return {'REJECT'} end
 if tonumber(redis.call('HGET',KEYS[1],'generation') or '0')>=1000 then redis.call('HSET',KEYS[1],'revoked','1');return {'REJECT'} end
 redis.call('HSET',KEYS[1],'used:'..ARGV[1],'1','previous',ARGV[1],'request',ARGV[2],'receipt',ARGV[4],'receipt_until',ms+tonumber(ARGV[6]),'current',ARGV[3])
 redis.call('HINCRBY',KEYS[1],'generation',1);return {'OK'}
`)

func (s *Service) Refresh(ctx context.Context, raw, key string) (Session, error) {
	if s.Config.Mode != "jwt" {
		return Session{}, fault.New("REFRESH_DISABLED", 409)
	}
	parsed, e := uuid.Parse(key)
	if e != nil || parsed.String() != key {
		return Session{}, fault.New("INVALID_REQUEST", 400)
	}
	family, secret, ok := strings.Cut(raw, ".")
	if !ok || !ValidToken(family) || !ValidToken(secret) {
		return Session{}, fault.New("UNAUTHORIZED", 401)
	}
	uid, e := s.Redis.HGet(ctx, s.familyKey(family), "uid").Result()
	if e == redis.Nil {
		return Session{}, fault.New("UNAUTHORIZED", 401)
	}
	if e != nil {
		return Session{}, fault.Dependency(e)
	}
	id, e := strconv.ParseInt(uid, 10, 64)
	if e != nil || id <= 0 {
		return Session{}, fault.Dependency(fmt.Errorf("invalid JWT family"))
	}
	if _, e = s.User(ctx, id); e != nil {
		return Session{}, e
	}
	next := randomToken()
	out, e := s.jwtSession(id, family, next)
	if e != nil {
		return Session{}, fault.Dependency(e)
	}
	receipt, e := s.seal(out, family+":"+key)
	if e != nil {
		return Session{}, fault.Dependency(e)
	}
	result, e := rotateJWT.Run(ctx, s.Redis, []string{s.familyKey(family)}, digest(secret), key, digest(next), receipt, uid, min(int64(30000), max(int64(0), time.Until(out.expiresAt).Milliseconds()-200))).StringSlice()
	if e != nil {
		return Session{}, fault.Dependency(e)
	}
	switch result[0] {
	case "OK":
		return out, nil
	case "REPLAY":
		out, e = s.unseal(result[1], family+":"+key)
		return out, fault.Dependency(e)
	case "REUSE":
		return Session{}, fault.New("REFRESH_REUSED", 401)
	default:
		return Session{}, fault.New("UNAUTHORIZED", 401)
	}
}
