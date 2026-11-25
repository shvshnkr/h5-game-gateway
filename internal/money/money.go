// Package money keeps all balances in int64 minor units with a fixed scale
// of 1e4 (four decimal places), mirroring the "up to the fourth decimal
// place" convention of slot-aggregator seamless wallets. Floating point is
// never used for storage or arithmetic on balances.
package money

import "math"

// Scale is the number of decimal places stored in minor units.
const Scale = 4

// Unit is one whole currency unit in minor units.
const Unit int64 = 10000

// FromFloat converts a user/API supplied float (e.g. 12.3456) into minor
// units, rounding half away from zero (math.Round handles negatives).
func FromFloat(v float64) int64 {
	return int64(math.Round(v * float64(Unit)))
}

// ToFloat renders minor units as a float for API payloads.
func ToFloat(m int64) float64 {
	return float64(m) / float64(Unit)
}

// Add returns a+b guarding against int64 overflow.
func Add(a, b int64) (int64, bool) {
	s := a + b
	if (b > 0 && s < a) || (b < 0 && s > a) {
		return 0, false
	}
	return s, true
}
