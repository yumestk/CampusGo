package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/yumestk/CampusGo/internal/auth"
)

type Event struct {
	Type    string `json:"type"`
	UserID  int64  `json:"userId,string,omitempty"`
	Payload any    `json:"payload"`
}
type connection struct {
	ws    *websocket.Conn
	queue chan Event
	user  auth.User
	token string
}

// Hub owns only this process's sockets. Redis leases locate user -> node; node
// Pub/Sub routes best-effort invalidations. SQL remains the recovery source.
type Hub struct {
	Auth     *auth.Service
	Redis    *redis.Client
	Prefix   string
	Node     string
	mu       sync.Mutex
	clients  map[*connection]struct{}
	ready    bool
	total    int
	handlers sync.WaitGroup
}

func NewHub(s *auth.Service, namespace string) *Hub {
	return &Hub{Auth: s, Redis: s.Redis, Prefix: "campusgo:realtime:" + s.Config.Environment + ":" + namespace + ":" + s.Config.Namespace + ":", Node: uuid.NewString(), clients: map[*connection]struct{}{}}
}
func (h *Hub) routeKey(user int64) string { return h.Prefix + "user:" + strconv.FormatInt(user, 10) }
func (h *Hub) channel(node string) string { return h.Prefix + "node:" + node }

// Redis TIME keeps lease interpretation independent of application clock skew.
var leaseScript = redis.NewScript(`local t=redis.call('TIME'); local now=t[1]*1000+math.floor(t[2]/1000); redis.call('ZREMRANGEBYSCORE',KEYS[1],'-inf',now); redis.call('ZADD',KEYS[1],now+20000,ARGV[1]); redis.call('PEXPIRE',KEYS[1],25000); return 1`)
var locateScript = redis.NewScript(`local t=redis.call('TIME'); local now=t[1]*1000+math.floor(t[2]/1000); redis.call('ZREMRANGEBYSCORE',KEYS[1],'-inf',now); return redis.call('ZRANGE',KEYS[1],0,-1)`)

func (h *Hub) lease(ctx context.Context, user int64, member string) error {
	return leaseScript.Run(ctx, h.Redis, []string{h.routeKey(user)}, member).Err()
}
func (h *Hub) Publish(ctx context.Context, user int64, e Event) error {
	e.UserID = user
	body, err := json.Marshal(e)
	if err != nil {
		return err
	}
	nodes, err := locateScript.Run(ctx, h.Redis, []string{h.routeKey(user)}).StringSlice()
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, member := range nodes {
		node, _, _ := strings.Cut(member, ":")
		if seen[node] {
			continue
		}
		seen[node] = true
		if err = h.Redis.Publish(ctx, h.channel(node), body).Err(); err != nil {
			return err
		}
	}
	return nil
}
func (h *Hub) deliver(e Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.clients {
		if c.user.ID == e.UserID {
			select {
			case c.queue <- e:
			default:
				c.ws.CloseNow()
			}
		}
	}
}
func (h *Hub) Run(ctx context.Context) {
	for ctx.Err() == nil {
		h.runSession(ctx)
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Second):
		}
	}
}
func (h *Hub) runSession(ctx context.Context) {
	sub := h.Redis.Subscribe(ctx, h.channel(h.Node))
	defer sub.Close()
	check, cancel := context.WithTimeout(ctx, 2*time.Second)
	_, err := sub.Receive(check)
	cancel()
	if err != nil {
		return
	} // Never advertise a socket before its node subscription exists.
	h.mu.Lock()
	h.ready = true
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		h.ready = false
		for c := range h.clients {
			c.ws.CloseNow()
		}
		h.mu.Unlock()
		h.handlers.Wait()
	}()
	messages := sub.Channel(redis.WithChannelSize(128))
	for {
		select {
		case <-ctx.Done():
			return
		case m, ok := <-messages:
			if !ok {
				return
			}
			var e Event
			if len(m.Payload) <= 8192 && json.Unmarshal([]byte(m.Payload), &e) == nil {
				h.deliver(e)
			}
		}
	}
}
func (h *Hub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Explicit origin equality also rejects hostile same-host ports. Credentials
	// are sent in the first WS frame (WSS with HTTPS), never in URLs or logs.
	if h.Auth.Config.FrontendOrigin == "" || r.Header.Get("Origin") != h.Auth.Config.FrontendOrigin || r.URL.RawQuery != "" {
		rejectUpgrade(w, 403, "FORBIDDEN", "没有访问权限")
		return
	}
	h.mu.Lock()
	ready := h.ready && h.total < 1000
	if ready {
		h.total++
		h.handlers.Add(1)
	}
	h.mu.Unlock()
	if !ready {
		rejectUpgrade(w, 503, "DEPENDENCY_UNAVAILABLE", "实时服务暂不可用，请稍后重试")
		return
	}
	defer func() { h.mu.Lock(); h.total--; h.mu.Unlock(); h.handlers.Done() }()
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true}) // Exact origin checked above.
	if err != nil {
		return
	}
	defer ws.CloseNow()
	ws.SetReadLimit(4096)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	first, stop := context.WithTimeout(ctx, 3*time.Second)
	var hello struct {
		Token string `json:"token"`
	}
	err = wsjson.Read(first, ws, &hello)
	var u auth.User
	if err == nil {
		u, err = h.Auth.Resolve(first, hello.Token)
	}
	stop()
	if err != nil {
		ws.Close(websocket.StatusPolicyViolation, "authentication required")
		return
	}
	c := &connection{ws: ws, user: u, token: hello.Token, queue: make(chan Event, 16)}
	h.mu.Lock()
	count := 0
	for other := range h.clients {
		if other.user.ID == u.ID {
			count++
		}
	}
	if !h.ready || count >= 5 {
		h.mu.Unlock()
		ws.Close(websocket.StatusTryAgainLater, "connection limit")
		return
	}
	h.clients[c] = struct{}{}
	h.mu.Unlock()
	member := h.Node + ":" + uuid.NewString()
	defer func() {
		h.mu.Lock()
		delete(h.clients, c)
		h.mu.Unlock()
		clean, done := context.WithTimeout(context.Background(), time.Second)
		defer done()
		h.Redis.ZRem(clean, h.routeKey(u.ID), member)
	}()
	check, stop := context.WithTimeout(ctx, time.Second)
	err = h.lease(check, u.ID, member)
	stop()
	if err != nil {
		return
	}
	if err = writeEvent(ctx, ws, Event{Type: "ready", Payload: map[string]string{"node": h.Node}}); err != nil {
		return
	}
	// Exactly one reader handles protocol Ping/Pong; application input after auth
	// is prohibited. The browser automatically responds to control-frame Ping.
	readDone := make(chan struct{})
	go func() { defer close(readDone); _, _, _ = ws.Read(ctx); cancel() }()
	defer func() { cancel(); ws.CloseNow(); <-readDone }()
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	var lastFacts string
	for {
		select {
		case <-ctx.Done():
			return
		case e := <-c.queue:
			// Revalidate at delivery, not only on the timer: revoked credentials cannot
			// receive a late private hint during the periodic-check interval.
			if !h.valid(ctx, c) {
				ws.Close(websocket.StatusPolicyViolation, "authentication expired")
				return
			}
			if writeEvent(ctx, ws, e) != nil {
				return
			}
		case <-tick.C:
			if !h.valid(ctx, c) {
				ws.Close(websocket.StatusPolicyViolation, "authentication expired")
				return
			}
			work, done := context.WithTimeout(ctx, 2*time.Second)
			err = h.lease(work, u.ID, member)
			if err == nil {
				err = ws.Ping(work)
			}
			if err != nil {
				done()
				return
			}
			// Connected-user snapshot comparison covers all status and credit paths,
			// including timeout workers. Hints may be coalesced; no replay promise.
			// ponytail: one SQL snapshot per connection per 5s; coalesce by user
			// if D8 measurements show this dominates database load.
			facts, e := h.facts(work, u.ID)
			done()
			if e == nil && facts != lastFacts {
				lastFacts = facts
				if writeEvent(ctx, ws, Event{Type: "state.changed", Payload: nil}) != nil {
					return
				}
			}
		}
	}
}
func (h *Hub) valid(ctx context.Context, c *connection) bool {
	check, stop := context.WithTimeout(ctx, time.Second)
	defer stop()
	u, err := h.Auth.Resolve(check, c.token)
	return err == nil && u == c.user
}
func writeEvent(ctx context.Context, ws *websocket.Conn, e Event) error {
	work, done := context.WithTimeout(ctx, time.Second)
	defer done()
	return wsjson.Write(work, ws, e)
}
func (h *Hub) facts(ctx context.Context, user int64) (string, error) {
	var logID, creditVersion, noticeID int64
	err := h.Auth.DB.QueryRowContext(ctx, `SELECT COALESCE((SELECT MAX(l.id) FROM errand_status_log l JOIN errand e ON e.id=l.errand_id AND e.campus_id=l.campus_id WHERE e.publisher_id=? OR e.grabber_id=?),0),COALESCE((SELECT version FROM credit_score WHERE user_id=?),0),COALESCE((SELECT MAX(id) FROM notification WHERE user_id=?),0)`, user, user, user, user).Scan(&logID, &creditVersion, &noticeID)
	return fmt.Sprintf("%d:%d:%d", logID, creditVersion, noticeID), err
}

func rejectUpgrade(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if status == 503 {
		w.Header().Set("Retry-After", "1")
	}
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"code": code, "message": message, "data": nil})
}
