package contracts

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"testing"
)

// Golden bytes were emitted by the DTO declarations at the source commit, not
// by the refactored types. Field order participates in durable fingerprints.
func TestPreRefactorFundBytes(t *testing.T) {
	body, err := os.ReadFile("testdata/pre-refactor-funds.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		SourceCommit string
		Cases        []struct{ Kind, CommandJSON, Fingerprint, EventJSON string }
	}
	if err = json.Unmarshal(body, &fixture); err != nil {
		t.Fatal(err)
	}
	if fixture.SourceCommit != "3dfaab25f4294ace69c3e130e53221a82600c4f9" || len(fixture.Cases) != 5 {
		t.Fatal("missing legacy provenance/cases")
	}
	for _, c := range fixture.Cases {
		t.Run(c.Kind, func(t *testing.T) {
			var command FundCommand
			var event FundEvent
			if err := json.Unmarshal([]byte(c.CommandJSON), &command); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal([]byte(c.EventJSON), &event); err != nil {
				t.Fatal(err)
			}
			encoded, _ := json.Marshal(command)
			wire, _ := json.Marshal(event)
			if string(encoded) != c.CommandJSON || string(wire) != c.EventJSON {
				t.Fatal("legacy encoding changed")
			}
			if fmt.Sprintf("%x", sha256.Sum256(encoded)) != c.Fingerprint {
				t.Fatal("legacy fingerprint changed")
			}
		})
	}
}
