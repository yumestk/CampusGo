package errand

import "testing"

func TestTaskAuthorityAndReplay(t *testing.T) {
	owner, runner, other, arb := Actor{ID: 1, CampusID: 1, Role: "STUDENT"}, Actor{ID: 2, CampusID: 1, Role: "STUDENT"}, Actor{ID: 3, CampusID: 1, Role: "STUDENT"}, Actor{ID: 4, CampusID: 1, Role: "ARBITRATOR"}
	for _, tc := range []struct {
		status, action string
		actor          Actor
		code           string
	}{
		{"ACCEPTED", "PICK_UP", runner, ""}, {"PICKED_UP", "PICK_UP", runner, ""}, {"PICKED_UP", "PICK_UP", other, "FORBIDDEN"},
		{"PICKED_UP", "DELIVER", runner, ""}, {"DELIVERED", "DELIVER", runner, ""}, {"ACCEPTED", "DELIVER", runner, "INVALID_STATUS"},
		{"DELIVERED", "DISPUTE", owner, ""}, {"DISPUTED", "DISPUTE", runner, ""}, {"SETTLED", "DISPUTE", runner, "INVALID_STATUS"},
		{"DRAFT", "CANCEL_DRAFT", owner, ""}, {"CANCELLED", "CANCEL_DRAFT", other, "FORBIDDEN"},
	} {
		task := Restore(Snapshot{PublisherID: 1, RunnerID: 2, CampusID: 1, Status: tc.status})
		_, _, code := task.Fulfill(tc.actor, tc.action)
		if code != tc.code {
			t.Fatalf("%+v: %s", tc, code)
		}
	}
	task := Restore(Snapshot{PublisherID: 1, RunnerID: 2, CampusID: 1, Status: "DISPUTED", Round: 3})
	if task.AuthorizeFund(arb, "ARBITRATE_PUBLISHER") != "" || task.AuthorizeFund(runner, "ARBITRATE_RUNNER") != "FORBIDDEN" {
		t.Fatal("arbitration authority")
	}
	if task.Confirm(runner, 2) != "STALE_VERSION" || task.Confirm(other, 3) != "NOT_CURRENT_GRABBER" {
		t.Fatal("round/owner checks")
	}
	draft := Restore(Snapshot{PublisherID: 1, CampusID: 1, Status: "DRAFT"})
	if draft.Visible(other) || !draft.Visible(owner) || !draft.Visible(arb) || draft.Visible(Actor{ID: 1, CampusID: 2}) {
		t.Fatal("draft visibility")
	}
}
func TestAdmissionAndActionHintsHaveDifferentSemantics(t *testing.T) {
	task := Restore(Snapshot{PublisherID: 1, RunnerID: 2, CampusID: 1, Status: "LOCKED", Slots: 1, Round: 3, Version: 9007199254740993})
	a := Actor{ID: 3, CampusID: 1, Role: "STUDENT"}
	if got := task.Actions(a); len(got) != 1 || got[0] != "GRAB" {
		t.Fatal(got)
	}
	if task.Grab(a, 3, 9007199254740993) != "SLOT_FULL" {
		t.Fatal("hint bypassed authoritative admission")
	}
	if task.Grab(a, 2, 9007199254740993) != "STALE_VERSION" {
		t.Fatal("round precedence")
	}
	if Eligible(39, 0) != "CREDIT_TOO_LOW" || Eligible(40, 5) != "TOO_MANY_ONGOING" || Eligible(40, 4) != "" {
		t.Fatal("eligibility boundary")
	}
}
