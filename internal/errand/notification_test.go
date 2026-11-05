package errand

import (
	"strings"
	"testing"
)

func TestFundNoticeRecipientsAndPostings(t *testing.T) {
	for _, kind := range []string{"CANCEL", "SETTLE", "AUTO_SETTLE", "ARBITRATE_RUNNER", "ARBITRATE_PUBLISHER"} {
		t.Run(kind, func(t *testing.T) {
			e := FundEvent{Kind: kind, PublisherID: 1001, RunnerID: 2001, Amount: 1001}
			if fundStatus(kind) == "SETTLED" {
				e.Commission = 50
			}
			notices := FundNotices(e)
			if notices[1001] == "" {
				t.Fatal("missing publisher")
			}
			if kind == "CANCEL" {
				if len(notices) != 1 || !strings.Contains(notices[1001], "1001 分已退回") {
					t.Fatal(notices)
				}
			} else if len(notices) != 2 {
				t.Fatal("missing runner", notices)
			}
			if fundStatus(kind) == "SETTLED" && (!strings.Contains(notices[2001], "951 分") || !strings.Contains(notices[2001], "50 分")) {
				t.Fatal("net funds mismatch", notices)
			}
			if kind == "ARBITRATE_PUBLISHER" && (!strings.Contains(notices[2001], "未获得报酬") || !strings.Contains(notices[1001], "争议已裁决")) {
				t.Fatal("wrong arbitration outcome", notices)
			}
		})
	}
}
