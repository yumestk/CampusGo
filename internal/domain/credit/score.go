// Package credit owns score rules; event deduplication and locking live in storage.
package credit

const (
	Base         = 60
	Minimum      = 0
	Maximum      = 100
	WindowDays   = 30
	HistoryLimit = 50
)

func Delta(kind string) (int, bool) {
	switch kind {
	case "SETTLE":
		return 2, true
	case "GRAB_TIMEOUT_REVERT":
		return -5, true
	case "DISPUTE_LOSE":
		return -8, true
	}
	return 0, false
}
func Clamp(score int) int { return min(Maximum, max(Minimum, score)) }
func Description(kind string) string {
	return map[string]string{"SETTLE": "完成结算", "GRAB_TIMEOUT_REVERT": "抢中后超时未确认", "DISPUTE_LOSE": "争议败诉"}[kind]
}
