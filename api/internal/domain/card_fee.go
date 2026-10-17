package domain

// CardFeeRates are Mercado Pago's card fee parts (R26.5). Rates are basis
// points: 349 is 3.49%, 1600 is 16%.
type CardFeeRates struct {
	PercentBps int64 // MP's percentage of the charged total
	FixedCents int64 // MP's fixed fee per payment
	TaxBps     int64 // IVA charged on each of the two parts above
}

// Fee is what MP keeps from a payment of total cents. Like MP's simulator,
// it rounds each part to the cent half-up: the percentage, then the IVA on
// the percentage and the IVA on the fixed fee.
func (r CardFeeRates) Fee(total int64) int64 {
	percent := divHalfUp(total*r.PercentBps, 10_000)
	return percent + r.FixedCents +
		divHalfUp(percent*r.TaxBps, 10_000) +
		divHalfUp(r.FixedCents*r.TaxBps, 10_000)
}

// CardTotal is the smallest total a member pays by card so that, after MP's
// fee, amount + commission still reach the team (R26.5). cardFee is
// total - amount - commission: MP's fee plus the cent or so the rounding
// leaves over. Callers must pass a non-negative amount and commission, and
// rates whose PercentBps*(1+TaxBps) stays below 100%: at 100% this divides
// by zero, above it the search never ends.
func CardTotal(amount, commission int64, r CardFeeRates) (total, cardFee int64) {
	need := amount + commission
	for total = searchStart(need, r); total-r.Fee(total) < need; total++ {
	}
	return total, total - need
}

// searchStart is a total no larger than the answer, derived from the rates
// so a rate change needs no code change. The exact fee is
// T*k + F*(1+t) with k = p*(1+t); the three half-up roundings in Fee lower it
// by at most 2 cents, so no T below (need - 2 + F*(1+t)) / (1 - k) can reach
// need. Values are scaled by 10^8 (bps * bps) to stay in int64.
func searchStart(need int64, r CardFeeRates) int64 {
	taxed := 10_000 + r.TaxBps
	start := ((need-2)*100_000_000 + r.FixedCents*taxed*10_000) /
		(100_000_000 - r.PercentBps*taxed)
	// ponytail: a zero fee is the floor; it also keeps start sane for tiny needs.
	return max(start, need)
}

// divHalfUp divides two non-negative integers, rounding halves up.
func divHalfUp(n, d int64) int64 {
	return (2*n + d) / (2 * d)
}
