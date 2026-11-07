// Package errand owns task decisions. Snapshots must be rechecked under the
// database locks by the caller; these rules do not provide concurrency control.
package errand

type Actor struct {
	ID, CampusID int64
	Role         string
}
type Snapshot struct {
	ID, CampusID, PublisherID, RunnerID, Version int64
	Round, Slots                                 int
	Status                                       string
}
type Task struct{ state Snapshot }

// Restore accepts persisted facts, including terminal/historical states. It
// deliberately does not apply creation rules to existing rows.
func Restore(s Snapshot) Task     { return Task{state: s} }
func (t Task) Snapshot() Snapshot { return t.state }
func Eligible(score, ongoing int) string {
	if score < 40 {
		return "CREDIT_TOO_LOW"
	}
	if ongoing >= 5 {
		return "TOO_MANY_ONGOING"
	}
	return ""
}
func (t Task) Grab(a Actor, round int, version int64) string {
	s := t.state
	if s.CampusID != a.CampusID || s.PublisherID == a.ID {
		return "ERRAND_NOT_GRABBABLE"
	}
	if s.Round != round || s.Version != version {
		return "STALE_VERSION"
	}
	if s.RunnerID == a.ID && s.RunnerID != 0 {
		return "ALREADY_GRABBED"
	}
	if s.Status != "PUBLISHED" || s.Slots != 1 {
		if s.Status == "LOCKED" {
			return "SLOT_FULL"
		}
		return "ERRAND_NOT_GRABBABLE"
	}
	return ""
}
func (t Task) Confirm(a Actor, round int) string {
	s := t.state
	if s.Round != round {
		return "STALE_VERSION"
	}
	if s.RunnerID == 0 || s.RunnerID != a.ID {
		return "NOT_CURRENT_GRABBER"
	}
	if s.Status != "LOCKED" && s.Status != "ACCEPTED" {
		return "INVALID_STATUS"
	}
	return ""
}

// Fulfill returns the expected source and target, keeping permission checks
// before terminal replay. Database deadline/version fences remain in storage.
func (t Task) Fulfill(a Actor, action string) (from, target, code string) {
	s := t.state
	runner := s.RunnerID != 0 && s.RunnerID == a.ID
	switch action {
	case "PICK_UP":
		if !runner {
			return "", "", "FORBIDDEN"
		}
		from, target = "ACCEPTED", "PICKED_UP"
	case "DELIVER":
		if !runner {
			return "", "", "FORBIDDEN"
		}
		from, target = "PICKED_UP", "DELIVERED"
	case "DISPUTE":
		if !runner && s.PublisherID != a.ID {
			return "", "", "FORBIDDEN"
		}
		target = "DISPUTED"
		if s.Status != "ACCEPTED" && s.Status != "PICKED_UP" && s.Status != "DELIVERED" && s.Status != target {
			return "", "", "INVALID_STATUS"
		}
		from = s.Status
	case "CANCEL_DRAFT":
		if s.PublisherID != a.ID {
			return "", "", "FORBIDDEN"
		}
		from, target = "DRAFT", "CANCELLED"
	default:
		return "", "", "INVALID_REQUEST"
	}
	if s.Status != from && s.Status != target {
		return "", "", "INVALID_STATUS"
	}
	return
}
func FundStatus(action string) string {
	switch action {
	case "CANCEL":
		return "CANCELLED"
	case "ARBITRATE_PUBLISHER":
		return "REFUNDED"
	default:
		return "SETTLED"
	}
}
func (t Task) AuthorizeFund(a Actor, action string) string {
	s := t.state
	switch action {
	case "SETTLE", "CANCEL":
		if a.ID != s.PublisherID {
			return "FORBIDDEN"
		}
	case "ARBITRATE_RUNNER", "ARBITRATE_PUBLISHER":
		if a.Role != "ARBITRATOR" {
			return "FORBIDDEN"
		}
	case "AUTO_SETTLE":
		if a.ID != -1 {
			return "FORBIDDEN"
		}
	default:
		return "INVALID_REQUEST"
	}
	allowed := false
	switch action {
	case "CANCEL":
		allowed = s.Status == "PUBLISHED"
	case "SETTLE", "AUTO_SETTLE":
		allowed = s.Status == "DELIVERED"
	case "ARBITRATE_RUNNER", "ARBITRATE_PUBLISHER":
		allowed = s.Status == "DISPUTED"
	}
	if !allowed {
		return "INVALID_STATUS"
	}
	return ""
}
func (t Task) Visible(a Actor) bool {
	s := t.state
	return s.CampusID == a.CampusID && (s.Status != "DRAFT" || s.PublisherID == a.ID || a.Role == "ARBITRATOR")
}
func (t Task) Actions(a Actor) []string {
	s := t.state
	out := []string{}
	if s.CampusID != a.CampusID {
		return out
	}
	runner := s.RunnerID != 0 && s.RunnerID == a.ID
	if (s.Status == "PUBLISHED" || (s.Status == "LOCKED" && !runner)) && s.PublisherID != a.ID && (a.Role == "STUDENT" || a.Role == "ARBITRATOR") {
		out = append(out, "GRAB")
	}
	if runner && s.Status == "LOCKED" {
		out = append(out, "CONFIRM")
	}
	if runner && s.Status == "ACCEPTED" {
		out = append(out, "PICK_UP")
	}
	if runner && s.Status == "PICKED_UP" {
		out = append(out, "DELIVER")
	}
	if (runner || s.PublisherID == a.ID) && (s.Status == "ACCEPTED" || s.Status == "PICKED_UP" || s.Status == "DELIVERED") {
		out = append(out, "DISPUTE")
	}
	if s.PublisherID == a.ID {
		switch s.Status {
		case "DELIVERED":
			out = append(out, "SETTLE")
		case "DRAFT":
			out = append(out, "CANCEL_DRAFT")
		case "PUBLISHED":
			out = append(out, "CANCEL")
		}
	}
	if a.Role == "ARBITRATOR" && s.Status == "DISPUTED" {
		out = append(out, "ARBITRATE_RUNNER", "ARBITRATE_PUBLISHER")
	}
	return out
}
