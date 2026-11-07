package application

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/yumestk/CampusGo/internal/contracts"
	fault "github.com/yumestk/CampusGo/internal/failure"
)

type PublishingStore interface {
	ApplyPublish(context.Context, contracts.Actor, string, contracts.PublishCommand, []byte) (contracts.PublishResult, error)
	Result(context.Context, contracts.Actor, string) (contracts.PublishResult, error)
}
type Publisher struct{ Store PublishingStore }

func (p *Publisher) Publish(ctx context.Context, u contracts.Actor, key string, c contracts.PublishCommand) (contracts.PublishResult, error) {
	if !contracts.ValidRequestKey(key) {
		return contracts.PublishResult{}, fault.New("INVALID_REQUEST", 400)
	}
	if err := validatePublish(&c, u); err != nil {
		return contracts.PublishResult{}, err
	}
	body, _ := json.Marshal(c)
	hash := sha256.Sum256(body)
	return p.Store.ApplyPublish(ctx, u, key, c, hash[:])
}
func (p *Publisher) Result(ctx context.Context, u contracts.Actor, key string) (contracts.PublishResult, error) {
	return p.Store.Result(ctx, u, key)
}
func validatePublish(c *contracts.PublishCommand, u contracts.Actor) error {
	if c.CampusID != u.CampusID {
		return fault.New("FORBIDDEN", 403)
	}
	c.Title = strings.TrimSpace(c.Title)
	if c.RewardCents <= 0 || c.SlotTotal != 1 || !utf8.ValidString(c.Title) || utf8.RuneCountInString(c.Title) > 64 || c.Title == "" || strings.ContainsFunc(c.Title, unicode.IsControl) {
		return fault.New("INVALID_REQUEST", 400)
	}
	switch c.Type {
	case "DELIVERY", "BUY", "QUEUE", "OTHER":
	default:
		return fault.New("INVALID_REQUEST", 400)
	}
	return nil
}
