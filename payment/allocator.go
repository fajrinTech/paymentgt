// Package payment implements amount allocation, settlement matching, and the
// reconciliation orchestrator shared by every provider adapter.
package payment

import (
	"github.com/hirotomasato/paygateme/core"
)

// AmountAllocator allocates a unique whole-rupiah offset so concurrent orders
// can be told apart purely by the amount that lands in the merchant account.
//
// Uniqueness is enforced on the resulting amount (baseAmount + offset), not on
// the offset alone. Scoping per base amount is not enough: `3500+1` and
// `3499+2` both settle at `3501`.
type AmountAllocator struct {
	minOffset  int64
	maxOffset  int64
	allowReuse bool
}

// NewAmountAllocator creates an allocator with the given offset window
// [1, maxOffset]. maxOffset must be positive.
func NewAmountAllocator(maxOffset int64) (*AmountAllocator, error) {
	if maxOffset < 1 {
		return nil, core.NewConfigError("maxOffset must be a positive integer", nil)
	}
	return &AmountAllocator{minOffset: 1, maxOffset: maxOffset}, nil
}

// NewAmountAllocatorWithZero creates an allocator that checks 0 first (exact amount),
// falling back to [1, maxOffset] if 0 is already claimed by an active payment.
func NewAmountAllocatorWithZero(maxOffset int64) (*AmountAllocator, error) {
	if maxOffset < 0 {
		return nil, core.NewConfigError("maxOffset must be non-negative", nil)
	}
	return &AmountAllocator{minOffset: 0, maxOffset: maxOffset}, nil
}

// NewExactAmountAllocator creates an allocator that strictly produces exact,
// unpadded amounts (offset 0 only). If allowReuse is true, it always hands out
// offset 0 even if the slot is currently claimed.
func NewExactAmountAllocator(allowReuse ...bool) *AmountAllocator {
	reuse := false
	if len(allowReuse) > 0 && allowReuse[0] {
		reuse = true
	}
	return &AmountAllocator{minOffset: 0, maxOffset: 0, allowReuse: reuse}
}

// DefaultAmountAllocator returns an allocator with the default offset window.
func DefaultAmountAllocator() *AmountAllocator {
	a, _ := NewAmountAllocator(core.DefaultMaxUniqueOffset)
	return a
}

// Min returns the smallest offset this allocator will hand out.
func (a *AmountAllocator) Min() int64 { return a.minOffset }

// Max returns the largest offset this allocator will hand out.
func (a *AmountAllocator) Max() int64 { return a.maxOffset }

// Allocate finds the smallest offset in [minOffset, maxOffset] whose resulting amount
// (baseAmount + offset) is not claimed by an active payment. It returns a
// CodeAmountPoolExhausted error when every slot in range is claimed.
func (a *AmountAllocator) Allocate(baseAmount int64, taken map[int64]bool) (int64, error) {
	if a.allowReuse {
		return 0, nil
	}
	for offset := a.minOffset; offset <= a.maxOffset; offset++ {
		if !taken[baseAmount+offset] {
			return offset, nil
		}
	}
	return 0, core.NewBaseErrorf(core.CodeAmountPoolExhausted,
		"No free unique amount slot available for base amount %d (offset window %d..%d is fully claimed)",
		baseAmount, a.minOffset, a.maxOffset)
}
