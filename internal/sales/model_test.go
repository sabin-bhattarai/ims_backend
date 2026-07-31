package sales_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/sabin-bhattarai/ims_backend/internal/sales"
)

func TestSalesOrderStateMachine(t *testing.T) {
	t.Parallel()

	// The happy path must be walkable end to end.
	path := []sales.OrderStatus{
		sales.OrderDraft, sales.OrderConfirmed, sales.OrderPicking,
		sales.OrderPacked, sales.OrderShipped, sales.OrderDelivered,
	}
	for i := 0; i < len(path)-1; i++ {
		assert.True(t, path[i].CanTransitionTo(path[i+1]),
			"%s → %s should be allowed", path[i], path[i+1])
	}

	forbidden := []struct {
		from, to sales.OrderStatus
		why      string
	}{
		{sales.OrderDraft, sales.OrderShipped, "stock must be reserved and picked first"},
		{sales.OrderDraft, sales.OrderPicking, "picking requires confirmation"},
		{sales.OrderConfirmed, sales.OrderShipped, "shipping requires picking and packing"},
		{sales.OrderShipped, sales.OrderCancelled, "shipped stock is gone; that is a return, not a cancellation"},
		{sales.OrderDelivered, sales.OrderCancelled, "a delivered order is final"},
		{sales.OrderCancelled, sales.OrderConfirmed, "a cancelled order is final"},
		{sales.OrderShipped, sales.OrderPacked, "fulfilment does not run backwards"},
	}
	for _, tc := range forbidden {
		assert.False(t, tc.from.CanTransitionTo(tc.to), "%s → %s must be refused: %s", tc.from, tc.to, tc.why)
	}
}

func TestCancellationIsAllowedBeforeShipping(t *testing.T) {
	t.Parallel()

	for _, from := range []sales.OrderStatus{
		sales.OrderDraft, sales.OrderConfirmed, sales.OrderPicking, sales.OrderPacked,
	} {
		assert.True(t, from.CanTransitionTo(sales.OrderCancelled),
			"%s should still be cancellable", from)
	}
}
