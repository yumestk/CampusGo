// Package wallet owns integer posting decisions, independent of SQL locking.
package wallet

import "math"

// Commission avoids overflow before division and rounds down to whole cents.
func Commission(amount int64) int64 { return amount/10000*500 + (amount%10000)*500/10000 }

type Allocation struct{ Amount, Recipient, Commission int64 }

func Allocate(amount int64, settle bool) Allocation {
	c := int64(0)
	if settle {
		c = Commission(amount)
	}
	return Allocation{amount, amount - c, c}
}

// Apply is evaluated on a locked account. Persisting balance and immutable
// postings in one transaction remains the adapter's responsibility.
func Apply(available, version, amount int64, debit bool) (int64, string) {
	if version == math.MaxInt64 {
		return 0, "ACCOUNT_SEQUENCE_EXHAUSTED"
	}
	if amount < 0 {
		return 0, "INVALID_AMOUNT"
	}
	if debit {
		if available < amount {
			return 0, "ESCROW_UNDERFUNDED"
		}
		return available - amount, ""
	}
	if available > math.MaxInt64-amount {
		return 0, "BALANCE_LIMIT"
	}
	return available + amount, ""
}
