package contracts

type RepairTask struct {
	ID           int64  `json:"id,string"`
	CampusID     int64  `json:"campusId,string"`
	ErrandID     int64  `json:"errandId,string"`
	EventKey     string `json:"eventKey"`
	Kind         string `json:"kind"`
	RequestID    *int64 `json:"requestId,string"`
	State        string `json:"state"`
	Attempts     int    `json:"attempts"`
	ReplayCount  int    `json:"replayCount"`
	WorkerToken  string `json:"-"`
	LastError    string `json:"lastError"`
	ReplayReason string `json:"replayReason"`
}
