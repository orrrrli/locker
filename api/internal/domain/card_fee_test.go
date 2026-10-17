package domain

import "testing"

func TestCardTotalSmoke(t *testing.T) {
	rates := CardFeeRates{PercentBps: 349, FixedCents: 400, TaxBps: 1600}
	total, cardFee := CardTotal(2000, 0, rates)
	if total != 2568 || cardFee != 568 {
		t.Fatalf("CardTotal(2000, 0) = %d, %d; want 2568, 568", total, cardFee)
	}
}
