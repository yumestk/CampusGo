package wire

import (
	"encoding/json"
	"testing"
)

func TestIntegerPrecision(t *testing.T) {
	for _, input := range []string{`9007199254740993`, `"9007199254740993"`, `9223372036854775807`, `"-9223372036854775808"`} {
		var value Integer
		if err := json.Unmarshal([]byte(input), &value); err != nil {
			t.Fatal(err)
		}
		encoded, _ := json.Marshal(value)
		var round Integer
		if err := json.Unmarshal(encoded, &round); err != nil || round != value {
			t.Fatal(string(encoded), err)
		}
	}
	for _, input := range []string{`null`, `1.1`, `1e3`, `9223372036854775808`, `"1.0"`, `true`} {
		var value Integer
		if json.Unmarshal([]byte(input), &value) == nil {
			t.Fatalf("accepted %s", input)
		}
	}
}
