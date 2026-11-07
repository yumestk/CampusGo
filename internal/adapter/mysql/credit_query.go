package mysql

import (
	"github.com/yumestk/CampusGo/internal/contracts"

	"context"
	"database/sql"
	"fmt"
	creditdomain "github.com/yumestk/CampusGo/internal/domain/credit"
	"strconv"

	"github.com/redis/go-redis/v9"
	"github.com/yumestk/CampusGo/internal/auth"
	"github.com/yumestk/CampusGo/internal/platform/fault"
)

func (g *CreditQuery) CreditSelf(ctx context.Context, u auth.User) (CreditProfile, error) {
	p := CreditProfile{Score: creditdomain.Base, WindowDays: creditdomain.WindowDays, Events: []CreditEvent{}}
	tx, err := g.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return p, fault.Dependency(err)
	}
	defer tx.Rollback()
	err = tx.QueryRowContext(ctx, "SELECT COALESCE((SELECT score FROM credit_score WHERE user_id=?),60),COALESCE((SELECT version FROM credit_score WHERE user_id=?),0)", u.ID, u.ID).Scan(&p.Score, &p.Version)
	if err != nil {
		return p, fault.Dependency(err)
	}
	rows, err := tx.QueryContext(ctx, "SELECT id,kind,delta,errand_id,created_at FROM credit_event WHERE user_id=? AND created_at>=TIMESTAMPADD(DAY,-30,CURRENT_TIMESTAMP(3)) ORDER BY created_at DESC,id DESC LIMIT 50", u.ID)
	if err != nil {
		return p, fault.Dependency(err)
	}
	for rows.Next() {
		var e CreditEvent
		if err = rows.Scan(&e.ID, &e.Type, &e.Delta, &e.RefID, &e.Time); err != nil {
			rows.Close()
			return p, fault.Dependency(err)
		}
		e.Description = creditdomain.Description(e.Type)
		p.Events = append(p.Events, e)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return p, fault.Dependency(err)
	}
	return p, fault.Dependency(tx.Commit())
}

// The ranking is a bounded display projection, never an eligibility authority.
// Rebuild the top 100 from SQL on cache miss; one Lua call replaces all members
// atomically. TTL bounds stale scores and repairs missed updates without dual writes.
var replaceCreditRank = redis.NewScript(`
redis.call('DEL',KEYS[1])
for i=1,#ARGV,2 do redis.call('ZADD',KEYS[1],ARGV[i],ARGV[i+1]) end
redis.call('EXPIRE',KEYS[1],30)
return 1`)

func (g *CreditQuery) CreditRanking(ctx context.Context, u auth.User, campus int64, limit int) ([]CreditRank, error) {
	if campus != u.CampusID {
		return nil, fault.New("FORBIDDEN", 403)
	}
	if limit < 1 || limit > 100 {
		return nil, fault.New("INVALID_REQUEST", 400)
	}
	key := fmt.Sprintf("campusgo:%s:credit:rank:%d", g.Namespace, campus)
	if g.Redis != nil {
		cached, err := g.Redis.ZRevRangeWithScores(ctx, key, 0, int64(limit-1)).Result()
		if err == nil && len(cached) > 0 {
			out := make([]CreditRank, 0, len(cached))
			for _, entry := range cached {
				id, e := strconv.ParseInt(fmt.Sprint(entry.Member), 10, 64)
				if e != nil || id <= 0 || entry.Score < 0 || entry.Score > 100 || entry.Score != float64(int(entry.Score)) {
					out = nil
					break
				}
				out = append(out, CreditRank{UserID: id, Score: int(entry.Score)})
			}
			if out != nil {
				return out, nil
			}
		}
	}
	rows, err := g.DB.QueryContext(ctx, `SELECT s.user_id,s.score FROM credit_score s JOIN app_user u ON u.id=s.user_id WHERE u.campus_id=? ORDER BY s.score DESC,CAST(s.user_id AS CHAR) COLLATE utf8mb4_bin DESC LIMIT 100`, campus)
	if err != nil {
		return nil, fault.Dependency(err)
	}
	out := []CreditRank{}
	args := []any{}
	for rows.Next() {
		var e CreditRank
		if err = rows.Scan(&e.UserID, &e.Score); err != nil {
			rows.Close()
			return nil, fault.Dependency(err)
		}
		out = append(out, e)
		args = append(args, e.Score, strconv.FormatInt(e.UserID, 10))
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, fault.Dependency(err)
	}
	if g.Redis != nil && len(args) > 0 {
		_ = replaceCreditRank.Run(ctx, g.Redis, []string{key}, args...).Err()
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

type CreditQuery struct {
	DB        *sql.DB
	Redis     *redis.Client
	Namespace string
}

type CreditEvent = contracts.CreditEvent

type CreditProfile = contracts.CreditProfile

type CreditRank = contracts.CreditRank
