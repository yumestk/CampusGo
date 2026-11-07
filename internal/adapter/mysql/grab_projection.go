package mysql

import (
	"context"
	"embed"
	"fmt"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// The v2 protocol is retained from Campus Dash, including string comparison of
// 64-bit versions: Redis Lua numbers cannot represent Snowflake IDs exactly.
//
//go:embed lua/*.lua
var grabScripts embed.FS
var projectionScripts = func() map[string]*redis.Script {
	scripts := map[string]*redis.Script{}
	for _, name := range []string{"acquire_pending", "apply_projection", "revoke_pending", "begin_projection_rebuild", "finish_projection_rebuild"} {
		data, err := grabScripts.ReadFile("lua/" + name + ".lua")
		if err != nil {
			panic(err)
		}
		scripts[name] = redis.NewScript(string(data))
	}
	return scripts
}()

func (g *TaskStore) stateKey(id int64) string {
	return fmt.Sprintf("campusgo:%s:grab:v2:{%d}:state", g.Namespace, id)
}
func (g *TaskStore) revokedKey(r grabRequest) string {
	return fmt.Sprintf("campusgo:%s:grab:v2:{%d}:revoked:%d:%s", g.Namespace, r.ErrandID, r.ID, r.Token)
}
func (g *TaskStore) script(ctx context.Context, name string, keys []string, args ...any) (int64, error) {
	return projectionScripts[name].Run(ctx, g.Redis, keys, args...).Int64()
}
func projectionArgs(c Card) []any {
	owner := ""
	if c.GrabberID != nil {
		owner = strconv.FormatInt(*c.GrabberID, 10)
	}
	return []any{strconv.FormatInt(c.Version, 10), strconv.Itoa(c.Round), c.Status, strconv.Itoa(c.SlotTaken), owner}
}
func (g *TaskStore) syncProjection(ctx context.Context, id int64) error {
	c, err := scan(g.DB.QueryRowContext(ctx, "SELECT "+columns+" FROM errand WHERE id=?", id))
	if err != nil {
		return err
	}
	args := append(projectionArgs(c), "0")
	code, err := g.script(ctx, "apply_projection", []string{g.stateKey(id)}, args...)
	if err != nil {
		return err
	}
	if code == 3 {
		// Claim INIT before reading SQL. An old event snapshot must never initialize a missing key.
		token := uuid.NewString()
		ok, e := g.script(ctx, "begin_projection_rebuild", []string{g.stateKey(id)}, token, 5000)
		if e != nil {
			return e
		}
		if ok != 1 {
			return fmt.Errorf("projection rebuild busy")
		}
		fresh, e := scan(g.DB.QueryRowContext(ctx, "SELECT "+columns+" FROM errand WHERE id=?", id))
		if e != nil {
			return e
		}
		ok, e = g.script(ctx, "finish_projection_rebuild", []string{g.stateKey(id)}, append([]any{token}, projectionArgs(fresh)...)...)
		if e != nil {
			return e
		}
		if ok != 1 {
			return fmt.Errorf("projection rebuild lease lost")
		}
	} else if code == 4 {
		fresh, e := scan(g.DB.QueryRowContext(ctx, "SELECT "+columns+" FROM errand WHERE id=?", id))
		if e != nil {
			return e
		}
		code, e = g.script(ctx, "apply_projection", []string{g.stateKey(id)}, append(projectionArgs(fresh), "1")...)
		if e != nil {
			return e
		}
		if code == 3 || code == 4 {
			return fmt.Errorf("projection conflict after SQL verification")
		}
	}
	return nil
}
func (g *TaskStore) acquire(ctx context.Context, r grabRequest) (int64, error) {
	return g.script(ctx, "acquire_pending", []string{g.stateKey(r.ErrandID), g.revokedKey(r)}, strconv.FormatInt(r.ID, 10), r.Token, strconv.FormatInt(r.BaseVersion, 10), strconv.Itoa(r.Round), strconv.FormatInt(r.RunnerID, 10), r.LeaseUntil.UnixMilli())
}
func (g *TaskStore) revoke(ctx context.Context, r grabRequest) error {
	_, err := g.script(ctx, "revoke_pending", []string{g.stateKey(r.ErrandID), g.revokedKey(r)}, strconv.FormatInt(r.ID, 10), r.Token, strconv.FormatInt(r.BaseVersion, 10), strconv.Itoa(r.Round), r.LeaseUntil.UnixMilli(), int64(time.Minute/time.Millisecond))
	return err
}
