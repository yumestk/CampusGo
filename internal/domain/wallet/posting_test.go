package wallet

import (
	"math"
	"math/big"
	"testing"
)

func TestAllocationIntegerBoundaries(t *testing.T) {
	for _, amount := range []int64{1, 19, 20, 1001, 10000, math.MaxInt64} {
		got := Allocate(amount, true)
		want := new(big.Int).Mul(big.NewInt(amount), big.NewInt(5))
		want.Div(want, big.NewInt(100))
		if got.Commission != want.Int64() || got.Recipient+got.Commission != amount {
			t.Fatal(amount, got, want)
		}
		refund := Allocate(amount, false)
		if refund.Recipient != amount || refund.Commission != 0 {
			t.Fatal(refund)
		}
	}
	if _, code := Apply(math.MaxInt64, 1, 1, false); code != "BALANCE_LIMIT" {
		t.Fatal(code)
	}
	if _, code := Apply(0, 1, 1, true); code != "ESCROW_UNDERFUNDED" {
		t.Fatal(code)
	}
	if _, code := Apply(10, math.MaxInt64, 1, false); code != "ACCOUNT_SEQUENCE_EXHAUSTED" {
		t.Fatal(code)
	}
	if n, code := Apply(math.MaxInt64-1, 1, 1, false); code != "" || n != math.MaxInt64 {
		t.Fatal(n, code)
	}
}
