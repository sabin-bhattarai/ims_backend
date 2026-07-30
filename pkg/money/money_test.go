package money_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sabin-bhattarai/ims-backend/pkg/money"
)

func TestLineTotal(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name                     string
		qty, price, disc, tax    string
		wantNet, wantTax, wantTotal string
	}{
		{
			name: "no discount or tax",
			qty:  "3", price: "10.00", disc: "0", tax: "0",
			wantNet: "30", wantTax: "0", wantTotal: "30",
		},
		{
			name: "tax only",
			qty:  "2", price: "50.00", disc: "0", tax: "13",
			wantNet: "100", wantTax: "13", wantTotal: "113",
		},
		{
			// Discount comes off before tax: tax is levied on what is charged.
			name: "discount applied before tax",
			qty:  "10", price: "20.00", disc: "10", tax: "13",
			wantNet: "180", wantTax: "23.4", wantTotal: "203.4",
		},
		{
			name: "fractional quantity",
			qty:  "2.5", price: "4.20", disc: "0", tax: "0",
			wantNet: "10.5", wantTax: "0", wantTotal: "10.5",
		},
		{
			// A third of a cent must not silently vanish or double-round.
			name: "repeating decimal rounds once at the end",
			qty:  "3", price: "0.3333", disc: "0", tax: "0",
			wantNet: "0.9999", wantTax: "0", wantTotal: "0.9999",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			net, tax, total := money.LineTotal(
				money.MustParse(tc.qty), money.MustParse(tc.price),
				money.MustParse(tc.disc), money.MustParse(tc.tax),
			)
			assert.True(t, net.Equal(money.MustParse(tc.wantNet)), "net: got %s want %s", net, tc.wantNet)
			assert.True(t, tax.Equal(money.MustParse(tc.wantTax)), "tax: got %s want %s", tax, tc.wantTax)
			assert.True(t, total.Equal(money.MustParse(tc.wantTotal)), "total: got %s want %s", total, tc.wantTotal)
		})
	}
}

func TestLineTotalIsExactWhereFloatIsNot(t *testing.T) {
	t.Parallel()

	// 0.1 + 0.2 != 0.3 in binary floating point. Summing a hundred such lines
	// is exactly how stock counts drift, so the decimal type must hold.
	sum := money.Zero()
	for i := 0; i < 100; i++ {
		_, _, total := money.LineTotal(
			money.MustParse("1"), money.MustParse("0.1"),
			money.Zero(), money.Zero(),
		)
		sum = sum.Add(total)
	}
	assert.True(t, sum.Equal(money.MustParse("10")), "expected exactly 10, got %s", sum)
}

func TestWeightedAverage(t *testing.T) {
	t.Parallel()

	t.Run("mixed costs", func(t *testing.T) {
		t.Parallel()
		// 10 @ 5.00 and 30 @ 9.00 → (50 + 270) / 40 = 8.00
		got := money.WeightedAverage([][2]money.Decimal{
			{money.MustParse("10"), money.MustParse("5.00")},
			{money.MustParse("30"), money.MustParse("9.00")},
		})
		assert.True(t, got.Equal(money.MustParse("8")), "got %s", got)
	})

	t.Run("zero quantity returns zero rather than dividing by zero", func(t *testing.T) {
		t.Parallel()
		got := money.WeightedAverage([][2]money.Decimal{
			{money.Zero(), money.MustParse("5.00")},
		})
		assert.True(t, got.IsZero(), "got %s", got)
	})

	t.Run("empty input", func(t *testing.T) {
		t.Parallel()
		assert.True(t, money.WeightedAverage(nil).IsZero())
	})
}

func TestRoundToPersistedScale(t *testing.T) {
	t.Parallel()

	// numeric(18,4) is the storage type, so anything finer must be rounded
	// before it is written or the database will do it for us silently.
	got := money.Round(money.MustParse("1.234567"))
	assert.Equal(t, "1.2346", got.String())
}

func TestSignHelpers(t *testing.T) {
	t.Parallel()

	assert.True(t, money.IsPositive(money.MustParse("0.0001")))
	assert.False(t, money.IsPositive(money.Zero()))
	assert.True(t, money.IsNegative(money.MustParse("-1")))
	assert.True(t, money.IsZero(money.Zero()))
	assert.True(t, money.Min(money.New(2), money.New(5)).Equal(money.New(2)))
	assert.True(t, money.Max(money.New(2), money.New(5)).Equal(money.New(5)))
	assert.True(t, money.Sum(money.New(1), money.New(2), money.New(3)).Equal(money.New(6)))
}

func TestJSONMarshalsAsNumber(t *testing.T) {
	t.Parallel()

	// The generated TypeScript and Dart clients expect `number`/`double`, not a
	// quoted string, so this encoding is part of the API contract.
	payload := struct {
		Qty money.Decimal `json:"qty"`
	}{Qty: money.MustParse("12.5000")}

	out, err := json.Marshal(payload)
	require.NoError(t, err)
	assert.JSONEq(t, `{"qty":12.5}`, string(out))
}
