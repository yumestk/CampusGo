package mysql

import (
	"github.com/yumestk/CampusGo/internal/contracts"

	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

func (g *TaskStore) candidateKeys(id int64) []string {
	return []string{fmt.Sprintf("campusgo:%s:candidates:{%d}", g.Namespace, id), fmt.Sprintf("campusgo:%s:candidate-meta:{%d}", g.Namespace, id)}
}

var offerCandidate = redis.NewScript(`
local current=redis.call('HGET',KEYS[2],ARGV[1])
if current and tonumber(string.match(current,'^(%d+):'))>=tonumber(ARGV[3]) then return 0 end
redis.call('ZADD',KEYS[1],ARGV[2],ARGV[1])
redis.call('HSET',KEYS[2],ARGV[1],ARGV[3]..':'..ARGV[4])
return 1`)
var peekCandidate = redis.NewScript(`
local head=redis.call('ZRANGE',KEYS[1],0,0)
if #head==0 then return {} end
local meta=redis.call('HGET',KEYS[2],head[1])
if not meta then redis.call('ZREM',KEYS[1],head[1]); return {'RETRY'} end
return {head[1],meta}`)
var removeCandidate = redis.NewScript(`
if redis.call('HGET',KEYS[2],ARGV[1])~=ARGV[2] then return 0 end
redis.call('ZREM',KEYS[1],ARGV[1]);redis.call('HDEL',KEYS[2],ARGV[1]);return 1`)

func (g *TaskStore) offerCandidate(ctx context.Context, r grabRequest) error {
	var score int
	// A late rejection can only offer for its original live cycle/round. Eligibility
	// is checked again under the user lock at promotion; this queue is a preference.
	err := g.DB.QueryRowContext(ctx, `SELECT COALESCE((SELECT score FROM credit_score WHERE user_id=?),60) FROM errand e JOIN app_user u ON u.id=? WHERE e.id=? AND e.campus_id=? AND u.campus_id=e.campus_id AND u.active=1 AND u.role IN ('STUDENT','ARBITRATOR') AND e.status='LOCKED' AND e.round=? AND e.publisher_id<>u.id AND e.grabber_id<>u.id AND (SELECT COUNT(*) FROM errand WHERE grabber_id=u.id AND status IN ('ACCEPTED','PICKED_UP'))<5`, r.RunnerID, r.RunnerID, r.ErrandID, r.CampusID, r.Round).Scan(&score)
	if err != nil {
		return err
	}
	if score < 40 {
		return nil
	}
	return offerCandidate.Run(ctx, g.Redis, g.candidateKeys(r.ErrandID), fmt.Sprint(r.RunnerID), float64(time.Now().UnixMilli())-float64(min(score, 100))*10, r.Round, uuid.NewString()).Err()
}
func (g *TaskStore) peekCandidate(ctx context.Context, id int64) (*Candidate, error) {
	row, err := peekCandidate.Run(ctx, g.Redis, g.candidateKeys(id)).StringSlice()
	if err != nil {
		return nil, err
	}
	if len(row) == 0 {
		return nil, nil
	}
	if len(row) == 1 {
		return nil, fmt.Errorf("candidate metadata missing; retry")
	}
	runner, e := strconv.ParseInt(row[0], 10, 64)
	if e != nil {
		return nil, e
	}
	parts := strings.SplitN(row[1], ":", 2)
	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid candidate metadata")
	}
	round, e := strconv.Atoi(parts[0])
	if e != nil {
		return nil, e
	}
	return &Candidate{RunnerID: runner, Round: round, Token: parts[1]}, nil
}
func (g *TaskStore) removeCandidate(ctx context.Context, id int64, c Candidate) error {
	return removeCandidate.Run(ctx, g.Redis, g.candidateKeys(id), fmt.Sprint(c.RunnerID), fmt.Sprintf("%d:%s", c.Round, c.Token)).Err()
}

type Candidate = contracts.Candidate
