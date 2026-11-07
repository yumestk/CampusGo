package contracts

import "time"

type Card struct {
	ID               int64      `json:"id,string"`
	CampusID         int64      `json:"campusId,string"`
	PublisherID      int64      `json:"publisherId,string"`
	GrabberID        *int64     `json:"grabberId,string"`
	Type             string     `json:"type"`
	Title            string     `json:"title"`
	RewardCents      int64      `json:"rewardCents,string"`
	SlotTotal        int        `json:"slotTotal"`
	SlotTaken        int        `json:"slotTaken"`
	Status           string     `json:"status"`
	Round            int        `json:"round"`
	Version          int64      `json:"version,string"`
	Role             string     `json:"role"`
	AvailableActions []string   `json:"availableActions"`
	ConfirmDeadline  *time.Time `json:"confirmDeadline"`
	DeliveredAt      *time.Time `json:"deliveredAt"`
	CreatedAt        time.Time  `json:"-"`
}
type GrabCommand struct {
	ErrandID      int64 `json:"errandId,string"`
	ExpectedRound int   `json:"expectedRound"`
}
type GrabResult struct {
	RequestKey       string `json:"requestKey"`
	ErrandID         int64  `json:"errandId,string"`
	State            string `json:"state"`
	ErrorCode        string `json:"errorCode"`
	AppliedRound     int    `json:"appliedRound"`
	RetryAfterMillis int    `json:"retryAfterMillis"`
}
type GrabRequest struct {
	ID, CampusID, RunnerID, ErrandID, BaseVersion int64
	Key, Token, State, Code                       string
	Fingerprint                                   []byte
	Round                                         int
	LeaseUntil                                    time.Time
}
type Candidate struct {
	RunnerID int64
	Round    int
	Token    string
}
type PublishCommand struct {
	CampusID    int64  `json:"campusId,string"`
	Type        string `json:"type"`
	Title       string `json:"title"`
	RewardCents int64  `json:"rewardCents,string"`
	SlotTotal   int    `json:"slotTotal"`
}
type PublishResult struct {
	RequestKey  string `json:"requestKey"`
	State       string `json:"state"`
	ErrandID    *int64 `json:"errandId,string"`
	Status      string `json:"status"`
	FrozenCents int64  `json:"frozenCents,string"`
	ErrorCode   string `json:"errorCode"`
}
type TimeoutMessage struct {
	Topic        string    `json:"topic"`
	ID           int64     `json:"id,string"`
	CampusID     int64     `json:"campusId,string"`
	ErrandID     int64     `json:"errandId,string"`
	Key          string    `json:"key"`
	Payload      []byte    `json:"-"`
	DeliverAt    time.Time `json:"deliverAt"`
	State        string    `json:"state"`
	Attempts     int       `json:"attempts"`
	ReplayCount  int       `json:"replayCount"`
	Token        string    `json:"-"`
	LastError    string    `json:"lastError"`
	ReplayReason string    `json:"replayReason"`
}

func (r GrabRequest) Result() GrabResult {
	retry := 0
	if r.State == "PROCESSING" {
		retry = 1000
	}
	return GrabResult{r.Key, r.ErrandID, r.State, r.Code, r.Round, retry}
}
