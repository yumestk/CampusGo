package errand

import (
	"encoding/base64"
	"testing"
	"time"

	"github.com/yumestk/CampusGo/internal/auth"
)

func TestQueryVisibility(t *testing.T) {
	owner := auth.User{ID: 1, CampusID: 1, Role: "STUDENT"}
	other := auth.User{ID: 2, CampusID: 1, Role: "STUDENT"}
	arb := auth.User{ID: 3, CampusID: 1, Role: "ARBITRATOR"}
	draft := Card{ID: 9, CampusID: 1, PublisherID: 1, Status: "DRAFT"}
	if !Visible(draft, owner) || Visible(draft, other) || !Visible(draft, arb) {
		t.Fatal("draft visibility")
	}
	other.CampusID = 2
	if Visible(Card{CampusID: 1, Status: "PUBLISHED"}, other) {
		t.Fatal("cross-campus visibility")
	}
	decorate(&draft, owner)
	if draft.Role != "PUBLISHER" || draft.AvailableActions == nil || len(draft.AvailableActions) != 1 || draft.AvailableActions[0] != "CANCEL_DRAFT" {
		t.Fatal("draft cancellation action missing")
	}
	for _, o := range []ListOptions{{CampusID: 2, Status: "PUBLISHED", Size: 20}, {CampusID: 1, Status: "PUBLISHED", Size: 101}, {CampusID: 1, Status: "anything", Size: 20}, {CampusID: 1, Role: "ARBITRATOR", Size: 20}, {CampusID: 1, Status: "PUBLISHED", Size: 20, Page: 1, Before: 9}} {
		if o.Validate(owner) == nil {
			t.Fatal("invalid query accepted")
		}
	}
}

func TestCompositeCursor(t *testing.T) {
	c := Card{ID: 9007199254740993, CreatedAt: time.UnixMilli(1793750400123).UTC()}
	timestamp, id, err := ParseCursor(cursor(c))
	if err != nil || !timestamp.Equal(c.CreatedAt) || id != c.ID {
		t.Fatal(timestamp, id, err)
	}
	for _, value := range []string{"invalid", "9007199254740993", base64.RawURLEncoding.EncodeToString([]byte("1793750400123:9223372036854775808")), base64.RawURLEncoding.EncodeToString([]byte("-1:1"))} {
		if _, _, err := ParseCursor(value); err == nil {
			t.Fatal("invalid cursor accepted")
		}
	}
}
