package wire

import (
	"bytes"
	"encoding/json"
	"strconv"
)

// Integer preserves signed 64-bit precision at the HTTP boundary. MQ has a separate contract.
type Integer int64

func (n Integer) MarshalJSON() ([]byte, error) { return json.Marshal(strconv.FormatInt(int64(n), 10)) }
func (n *Integer) UnmarshalJSON(data []byte) error {
	value := string(data)
	if bytes.HasPrefix(data, []byte(`"`)) {
		if err := json.Unmarshal(data, &value); err != nil {
			return err
		}
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err == nil {
		*n = Integer(parsed)
	}
	return err
}
