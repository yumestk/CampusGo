package contracts

import "time"

type CreditEvent struct {
	ID          int64     `json:"id,string"`
	Type        string    `json:"type"`
	Description string    `json:"description"`
	Delta       int       `json:"delta"`
	RefID       int64     `json:"refId,string"`
	Time        time.Time `json:"time"`
}
type CreditProfile struct {
	Score      int           `json:"score"`
	Version    int64         `json:"version,string"`
	WindowDays int           `json:"windowDays"`
	Events     []CreditEvent `json:"events"`
}
type CreditRank struct {
	UserID int64 `json:"userId,string"`
	Score  int   `json:"score"`
}
