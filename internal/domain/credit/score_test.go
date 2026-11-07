package credit

import "testing"

func TestActualCreditRulesOnly(t *testing.T) {
	for kind, want := range map[string]int{"SETTLE": 2, "GRAB_TIMEOUT_REVERT": -5, "DISPUTE_LOSE": -8} {
		if got, ok := Delta(kind); !ok || got != want || Description(kind) == "" {
			t.Fatal(kind, got, ok)
		}
	}
	for _, kind := range []string{"CANCEL_AFTER_GRAB", "DELIVERY_LATE", ""} {
		if _, ok := Delta(kind); ok {
			t.Fatal("dormant rule enabled", kind)
		}
	}
	if Clamp(Base+2) != 62 || Clamp(100+2) != 100 || Clamp(0-8) != 0 {
		t.Fatal("score bounds")
	}
	// Immediate clamping is not interchangeable with recalibration from a window.
	if Clamp(Clamp(99+2)-8) == Clamp(99+2-8) {
		t.Fatal("example must retain clamp path dependence")
	}
}
