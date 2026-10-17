package domain

import (
	"errors"
	"testing"
)

// mpRates are MP's published MX card rates, the config defaults.
var mpRates = CardFeeRates{PercentBps: 349, FixedCents: 400, TaxBps: 1600}

// TestCardTotalSimulator pins the totals MP's simulator shows (R26.5). The
// commission 232 rows are unverified until activation gate G2.
func TestCardTotalSimulator(t *testing.T) {
	for _, tc := range []struct{ amount, commission, total int64 }{
		{2000, 0, 2568}, {2500, 0, 3089}, {3089, 0, 3703}, {4230, 0, 4892}, {4500, 0, 5174},
		{2000, 232, 2810}, {2500, 232, 3331}, {3089, 232, 3945}, {4230, 232, 5134}, {4500, 232, 5415},
	} {
		total, cardFee := CardTotal(tc.amount, tc.commission, mpRates)
		if total != tc.total || cardFee != tc.total-tc.amount-tc.commission {
			t.Errorf("CardTotal(%d, %d) = %d, %d; want %d, %d",
				tc.amount, tc.commission, total, cardFee, tc.total, tc.total-tc.amount-tc.commission)
		}
	}
	// 5414 leaves the team one cent short of 4500, so it is never the total.
	if net := 5414 - mpRates.Fee(5414) - 232; net >= 4500 {
		t.Errorf("5414 nets %d, want below 4500", net)
	}
}

// TestCardTotalProperty checks every amount in the R26.5 range, 100..1_000_000
// cents, not a sample: the whole range runs in well under a second.
func TestCardTotalProperty(t *testing.T) {
	for _, commission := range []int64{0, 232} {
		for amount := int64(100); amount <= 1_000_000; amount++ {
			total, cardFee := CardTotal(amount, commission, mpRates)
			if total-mpRates.Fee(total)-commission < amount {
				t.Fatalf("amount %d, commission %d: total %d does not reach the amount", amount, commission, total)
			}
			for smaller := total - 3; smaller < total; smaller++ {
				if smaller-mpRates.Fee(smaller)-commission >= amount {
					t.Fatalf("amount %d, commission %d: %d already reaches it, total is %d", amount, commission, smaller, total)
				}
			}
			if amount+cardFee+commission != total {
				t.Fatalf("amount %d, commission %d: %d + %d + %d != %d", amount, commission, amount, cardFee, commission, total)
			}
		}
	}
}

// 5000 * 3.49% is exactly 174.5: half-up gives 175 (fee 667), half-even 174 (666).
func TestCardFeeRoundsHalfUp(t *testing.T) {
	if fee := mpRates.Fee(5000); fee != 667 {
		t.Fatalf("Fee(5000) = %d, want 667", fee)
	}
}

func TestCardFeeRatesValidate(t *testing.T) {
	for _, tc := range []struct {
		rates CardFeeRates
		want  error
	}{
		{mpRates, nil},
		{CardFeeRates{}, nil},
		{CardFeeRates{PercentBps: -1, FixedCents: 400, TaxBps: 1600}, ErrCardFeeNegative},
		{CardFeeRates{PercentBps: 349, FixedCents: -1, TaxBps: 1600}, ErrCardFeeNegative},
		{CardFeeRates{PercentBps: 349, FixedCents: 400, TaxBps: -1}, ErrCardFeeNegative},
		{CardFeeRates{TaxBps: 10_001}, ErrCardFeeTax},
		{CardFeeRates{PercentBps: 5000, TaxBps: 10_000}, ErrCardFeeRate},
		{CardFeeRates{PercentBps: 10_000}, ErrCardFeeRate},
	} {
		if err := tc.rates.Validate(); !errors.Is(err, tc.want) {
			t.Errorf("%+v: Validate() = %v, want %v", tc.rates, err, tc.want)
		}
	}
}
