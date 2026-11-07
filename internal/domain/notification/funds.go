package notification

import "fmt"

// Settlement is the notification input, separate from the durable MQ contract.
type Settlement struct {
	Kind                                      string
	PublisherID, RunnerID, Amount, Commission int64
}

func Funds(e Settlement) map[int64]string {
	pub := fmt.Sprintf("任务已退款，%d 分已退回你的账户", e.Amount)
	out := map[int64]string{}
	if e.Kind == "SETTLE" || e.Kind == "AUTO_SETTLE" || e.Kind == "ARBITRATE_RUNNER" {
		pub = fmt.Sprintf("任务已结算，跑腿实收 %d 分，平台佣金 %d 分", e.Amount-e.Commission, e.Commission)
		out[e.RunnerID] = fmt.Sprintf("任务已结算，你实收 %d 分，平台佣金 %d 分", e.Amount-e.Commission, e.Commission)
	}
	if e.Kind == "ARBITRATE_PUBLISHER" {
		out[e.RunnerID] = "争议已裁决：款项退回发单人，本次未获得报酬"
	}
	if e.Kind == "ARBITRATE_PUBLISHER" || e.Kind == "ARBITRATE_RUNNER" {
		pub = "争议已裁决：" + pub
	}
	out[e.PublisherID] = pub
	return out
}
