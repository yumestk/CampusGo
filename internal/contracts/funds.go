package contracts

type FundCommand struct {
	ErrandID int64  `json:"errandId,string"`
	Round    int    `json:"round"`
	Action   string `json:"action"`
}

type FundEvent struct {
	RequestID   string `json:"requestId"`
	CampusID    int64  `json:"campusId,string"`
	ErrandID    int64  `json:"errandId,string"`
	ActorID     int64  `json:"actorId,string"`
	PublisherID int64  `json:"publisherId,string"`
	RunnerID    int64  `json:"runnerId,string"`
	Round       int    `json:"round"`
	Kind        string `json:"kind"`
	Amount      int64  `json:"amountCents,string"`
	Commission  int64  `json:"commissionCents,string"`
}

type FundResult struct {
	RequestID string `json:"requestId"`
	State     string `json:"state"`
	ErrandID  int64  `json:"errandId,string"`
	Status    string `json:"status"`
	ErrorCode string `json:"errorCode"`
}
type FundRecovery struct {
	Event       FundEvent `json:"event"`
	Token       string    `json:"-"`
	Attempts    int       `json:"attempts"`
	State       string    `json:"state"`
	ReplayCount int       `json:"replayCount"`
	Reason      string    `json:"reason"`
	LastError   string    `json:"lastError"`
}
type DueSettlement struct {
	ID, CampusID int64
	Round        int
}
