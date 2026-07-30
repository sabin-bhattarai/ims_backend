// Package money provides the fixed-point decimal type used for every quantity
// and monetary amount in the system.
//
// Floating point is not an option here: FIFO valuation repeatedly adds and
// subtracts fractional quantities, and binary floats accumulate error that
// eventually shows up as stock that is "0.0000000001 units" short. All values
// map to Postgres numeric(18,4).
package money

import (
	"github.com/shopspring/decimal"
)

func init() {
	// Emit JSON numbers (1.25) rather than strings ("1.25") so the generated
	// TypeScript and Dart clients get `number`/`double` fields.
	decimal.MarshalJSONWithoutQuotes = true
	decimal.DivisionPrecision = 8
}

// Scale is the number of decimal places persisted, matching numeric(18,4).
const Scale = 4

// Decimal is the shared fixed-point type.
type Decimal = decimal.Decimal

// Zero is the additive identity.
func Zero() Decimal { return decimal.Zero }

// New builds a Decimal from a float. Use only for literals and test data;
// values arriving from the database or JSON are already exact.
func New(f float64) Decimal { return decimal.NewFromFloat(f) }

// FromInt builds a Decimal from a whole number.
func FromInt(i int64) Decimal { return decimal.NewFromInt(i) }

// Parse reads a decimal string such as "12.5000".
func Parse(s string) (Decimal, error) { return decimal.NewFromString(s) }

// MustParse is Parse for trusted constants; it panics on malformed input.
func MustParse(s string) Decimal { return decimal.RequireFromString(s) }

// Round rounds to the persisted scale. Apply before writing a computed total,
// so what is stored is exactly what was displayed.
func Round(d Decimal) Decimal { return d.Round(Scale) }

// IsPositive reports d > 0.
func IsPositive(d Decimal) bool { return d.GreaterThan(decimal.Zero) }

// IsNegative reports d < 0.
func IsNegative(d Decimal) bool { return d.LessThan(decimal.Zero) }

// IsZero reports d == 0.
func IsZero(d Decimal) bool { return d.IsZero() }

// Min returns the smaller of two values.
func Min(a, b Decimal) Decimal {
	if a.LessThan(b) {
		return a
	}
	return b
}

// Max returns the larger of two values.
func Max(a, b Decimal) Decimal {
	if a.GreaterThan(b) {
		return a
	}
	return b
}

// Sum adds a slice of values.
func Sum(values ...Decimal) Decimal {
	total := decimal.Zero
	for _, v := range values {
		total = total.Add(v)
	}
	return total
}

// LineTotal computes a document line total:
//
//	qty × unit_price, less discount_rate %, plus tax_rate % on the discounted
//	amount.
//
// Discount is applied before tax because tax authorities levy on the amount
// actually charged. The result is rounded once, at the end.
func LineTotal(qty, unitPrice, discountRate, taxRate Decimal) (net, tax, total Decimal) {
	hundred := decimal.NewFromInt(100)
	gross := qty.Mul(unitPrice)
	discount := gross.Mul(discountRate).Div(hundred)
	net = gross.Sub(discount)
	tax = net.Mul(taxRate).Div(hundred)
	return Round(net), Round(tax), Round(net.Add(tax))
}

// WeightedAverage computes the weighted average unit cost of quantity/cost
// pairs, returning zero when the total quantity is zero.
func WeightedAverage(pairs [][2]Decimal) Decimal {
	totalQty, totalValue := decimal.Zero, decimal.Zero
	for _, p := range pairs {
		qty, cost := p[0], p[1]
		totalQty = totalQty.Add(qty)
		totalValue = totalValue.Add(qty.Mul(cost))
	}
	if totalQty.IsZero() {
		return decimal.Zero
	}
	return Round(totalValue.Div(totalQty))
}
