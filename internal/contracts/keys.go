package contracts

import "github.com/google/uuid"

func ValidRequestKey(key string) bool {
	parsed, err := uuid.Parse(key)
	return err == nil && parsed != uuid.Nil && parsed.String() == key
}
