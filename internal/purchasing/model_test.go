package purchasing_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/sabin-bhattarai/ims_backend/internal/purchasing"
	"github.com/sabin-bhattarai/ims_backend/pkg/money"
)

func TestPurchaseOrderStateMachine(t *testing.T) {
	t.Parallel()

	allowed := []struct {
		from, to purchasing.Status
	}{
		{purchasing.StatusDraft, purchasing.StatusPendingApproval},
		{purchasing.StatusDraft, purchasing.StatusCancelled},
		{purchasing.StatusPendingApproval, purchasing.StatusApproved},
		{purchasing.StatusPendingApproval, purchasing.StatusRejected},
		{purchasing.StatusApproved, purchasing.StatusPartiallyReceived},
		{purchasing.StatusApproved, purchasing.StatusReceived},
		{purchasing.StatusPartiallyReceived, purchasing.StatusReceived},
		// A rejected order can be revised and resubmitted.
		{purchasing.StatusRejected, purchasing.StatusDraft},
	}
	for _, tc := range allowed {
		assert.True(t, tc.from.CanTransitionTo(tc.to), "%s → %s should be allowed", tc.from, tc.to)
	}

	forbidden := []struct {
		from, to purchasing.Status
		why      string
	}{
		{purchasing.StatusDraft, purchasing.StatusApproved, "approval requires submission first"},
		{purchasing.StatusDraft, purchasing.StatusReceived, "you cannot receive an unapproved order"},
		{purchasing.StatusPendingApproval, purchasing.StatusReceived, "receipt requires approval"},
		{purchasing.StatusReceived, purchasing.StatusCancelled, "a fully received order is final"},
		{purchasing.StatusReceived, purchasing.StatusDraft, "a fully received order is final"},
		{purchasing.StatusCancelled, purchasing.StatusApproved, "a cancelled order is final"},
		{purchasing.StatusCancelled, purchasing.StatusDraft, "a cancelled order is final"},
	}
	for _, tc := range forbidden {
		assert.False(t, tc.from.CanTransitionTo(tc.to), "%s → %s must be refused: %s", tc.from, tc.to, tc.why)
	}
}

func TestTerminalStatusesAcceptNothing(t *testing.T) {
	t.Parallel()

	every := []purchasing.Status{
		purchasing.StatusDraft, purchasing.StatusPendingApproval, purchasing.StatusApproved,
		purchasing.StatusRejected, purchasing.StatusPartiallyReceived,
		purchasing.StatusReceived, purchasing.StatusCancelled,
	}
	for _, to := range every {
		assert.False(t, purchasing.StatusReceived.CanTransitionTo(to), "received → %s", to)
		assert.False(t, purchasing.StatusCancelled.CanTransitionTo(to), "cancelled → %s", to)
	}
}

func TestLineOutstanding(t *testing.T) {
	t.Parallel()

	line := purchasing.PurchaseOrderLine{
		QuantityOrdered:  money.MustParse("100"),
		QuantityReceived: money.MustParse("40"),
	}
	assert.True(t, line.Outstanding().Equal(money.MustParse("60")))

	full := purchasing.PurchaseOrderLine{
		QuantityOrdered:  money.MustParse("100"),
		QuantityReceived: money.MustParse("100"),
	}
	assert.True(t, full.Outstanding().IsZero(), "a fully received line has nothing outstanding")
}
