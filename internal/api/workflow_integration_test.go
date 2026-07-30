//go:build integration

package api_test

import (
	"fmt"
	"net/http"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCoreInventoryWorkflow walks the acceptance-criteria path end to end:
// receive stock against an approved purchase order, store it, sell it, and see
// it reflected in the valuation report.
func TestCoreInventoryWorkflow(t *testing.T) {
	h := newHarness(t)

	admin := h.register("Workflow Co", uniqueEmail("admin"))

	// --- catalog and warehouse -----------------------------------------------
	warehouseID := h.id(h.do(http.MethodPost, "/api/v1/warehouses", admin, map[string]any{
		"code": "MAIN", "name": "Main Depot", "is_default": true,
	}))

	binResp := h.do(http.MethodPost, "/api/v1/warehouses/"+warehouseID+"/locations", admin, map[string]any{
		"code": "A-01", "kind": "bin",
	})
	require.Equal(t, http.StatusCreated, binResp.Status, "%v", binResp.Body)
	binID := h.id(binResp)

	productResp := h.do(http.MethodPost, "/api/v1/products", admin, map[string]any{
		"sku": "WIDGET-1", "name": "Blue Widget", "barcode": "1234567890128",
		"cost_price": 10, "sell_price": 25, "min_stock": 5, "reorder_quantity": 50,
	})
	require.Equal(t, http.StatusCreated, productResp.Status, "%v", productResp.Body)
	productID := h.id(productResp)

	supplierID := h.id(h.do(http.MethodPost, "/api/v1/suppliers", admin, map[string]any{
		"code": "SUP-1", "name": "Acme Supply", "lead_time_days": 5,
	}))

	// --- purchase order: draft → submit → approve ---------------------------
	poResp := h.do(http.MethodPost, "/api/v1/purchase-orders", admin, map[string]any{
		"supplier_id": supplierID, "warehouse_id": warehouseID,
		"lines": []map[string]any{
			{"product_id": productID, "quantity": 100, "unit_price": 10},
		},
	})
	require.Equal(t, http.StatusCreated, poResp.Status, "%v", poResp.Body)
	poID := h.id(poResp)
	assert.Equal(t, "draft", poResp.data()["status"])
	assert.Equal(t, 1000.0, num(t, poResp.data(), "grand_total"))

	// Receiving before approval must be refused: that guard is the whole point
	// of the approval workflow.
	early := h.do(http.MethodPost, "/api/v1/purchase-orders/"+poID+"/receive", admin, map[string]any{
		"lines": []map[string]any{{"purchase_order_line_id": uuid.NewString(), "quantity": 1}},
	})
	assert.Equal(t, http.StatusConflict, early.Status, "%v", early.Body)

	require.Equal(t, http.StatusOK,
		h.do(http.MethodPost, "/api/v1/purchase-orders/"+poID+"/submit", admin, nil).Status)
	approved := h.do(http.MethodPost, "/api/v1/purchase-orders/"+poID+"/approve", admin, nil)
	require.Equal(t, http.StatusOK, approved.Status, "%v", approved.Body)
	assert.Equal(t, "approved", approved.data()["status"])

	// --- partial receipt ----------------------------------------------------
	full := h.do(http.MethodGet, "/api/v1/purchase-orders/"+poID, admin, nil)
	lines := full.data()["lines"].([]any)
	require.Len(t, lines, 1)
	poLineID := lines[0].(map[string]any)["id"].(string)

	receipt := h.do(http.MethodPost, "/api/v1/purchase-orders/"+poID+"/receive", admin, map[string]any{
		"lines": []map[string]any{
			{"purchase_order_line_id": poLineID, "quantity": 60, "unit_cost": 10, "location_id": binID},
		},
	})
	require.Equal(t, http.StatusCreated, receipt.Status, "%v", receipt.Body)

	afterPartial := h.do(http.MethodGet, "/api/v1/purchase-orders/"+poID, admin, nil)
	assert.Equal(t, "partially_received", afterPartial.data()["status"],
		"60 of 100 received should leave the order partially received")

	// Over-receipt must be rejected rather than quietly inflating stock.
	over := h.do(http.MethodPost, "/api/v1/purchase-orders/"+poID+"/receive", admin, map[string]any{
		"lines": []map[string]any{{"purchase_order_line_id": poLineID, "quantity": 500}},
	})
	assert.Equal(t, http.StatusConflict, over.Status, "%v", over.Body)

	// Receive the rest at a higher cost, so FIFO has two distinct layers.
	rest := h.do(http.MethodPost, "/api/v1/purchase-orders/"+poID+"/receive", admin, map[string]any{
		"lines": []map[string]any{
			{"purchase_order_line_id": poLineID, "quantity": 40, "unit_cost": 15, "location_id": binID},
		},
	})
	require.Equal(t, http.StatusCreated, rest.Status, "%v", rest.Body)

	afterFull := h.do(http.MethodGet, "/api/v1/purchase-orders/"+poID, admin, nil)
	assert.Equal(t, "received", afterFull.data()["status"])

	// --- stock is on hand ---------------------------------------------------
	items := h.do(http.MethodGet, "/api/v1/stock/items?filter[product_id]="+productID, admin, nil)
	require.Equal(t, http.StatusOK, items.Status)
	onHand := 0.0
	for _, row := range items.dataList() {
		onHand += num(t, row.(map[string]any), "quantity")
	}
	assert.Equal(t, 100.0, onHand, "both receipts should be on hand")

	// --- barcode scan resolves the product with live stock ------------------
	scan := h.do(http.MethodGet, "/api/v1/products/scan/1234567890128", admin, nil)
	require.Equal(t, http.StatusOK, scan.Status, "%v", scan.Body)
	assert.Equal(t, "barcode", scan.data()["matched_on"])
	scanned := scan.data()["product"].(map[string]any)
	assert.Equal(t, "WIDGET-1", scanned["sku"])

	// --- sell: draft → confirm → pick → pack → ship -------------------------
	customerID := h.id(h.do(http.MethodPost, "/api/v1/customers", admin, map[string]any{
		"code": "CUS-1", "name": "Retail Buyer",
	}))

	soResp := h.do(http.MethodPost, "/api/v1/sales-orders", admin, map[string]any{
		"customer_id": customerID, "warehouse_id": warehouseID,
		"lines": []map[string]any{
			{"product_id": productID, "quantity": 70, "unit_price": 25},
		},
	})
	require.Equal(t, http.StatusCreated, soResp.Status, "%v", soResp.Body)
	soID := h.id(soResp)

	require.Equal(t, http.StatusOK,
		h.do(http.MethodPost, "/api/v1/sales-orders/"+soID+"/confirm", admin, nil).Status)
	for _, status := range []string{"picking", "packed"} {
		advance := h.do(http.MethodPost, "/api/v1/sales-orders/"+soID+"/advance", admin,
			map[string]any{"status": status})
		require.Equal(t, http.StatusOK, advance.Status, "advance to %s: %v", status, advance.Body)
	}

	shipped := h.do(http.MethodPost, "/api/v1/sales-orders/"+soID+"/ship", admin,
		map[string]any{"tracking_number": "TRACK-1"})
	require.Equal(t, http.StatusOK, shipped.Status, "%v", shipped.Body)
	assert.Equal(t, "shipped", shipped.data()["status"])

	// FIFO: 60 units at 10 then 10 units at 15 = 600 + 150 = 750.
	shippedFull := h.do(http.MethodGet, "/api/v1/sales-orders/"+soID, admin, nil)
	soLines := shippedFull.data()["lines"].([]any)
	require.Len(t, soLines, 1)
	assert.Equal(t, 750.0, num(t, soLines[0].(map[string]any), "cogs_total"),
		"COGS must consume the oldest, cheapest layer first")

	// --- valuation reflects what is left -----------------------------------
	// 30 units remain, all from the 15.00 layer → 450.00.
	valuation := h.do(http.MethodGet, "/api/v1/reports/valuation?method=fifo", admin, nil)
	require.Equal(t, http.StatusOK, valuation.Status, "%v", valuation.Body)
	assert.Equal(t, 450.0, num(t, valuation.data(), "total_value"))
	assert.Equal(t, 30.0, num(t, valuation.data(), "total_units"))

	// --- the dashboard agrees ----------------------------------------------
	dashboard := h.do(http.MethodGet, "/api/v1/reports/dashboard", admin, nil)
	require.Equal(t, http.StatusOK, dashboard.Status, "%v", dashboard.Body)
	assert.Equal(t, 450.0, num(t, dashboard.data(), "total_stock_value"))
	assert.Equal(t, 1.0, num(t, dashboard.data(), "total_products"))

	// --- the ledger recorded every step ------------------------------------
	movements := h.do(http.MethodGet, "/api/v1/stock/movements?filter[product_id]="+productID, admin, nil)
	require.Equal(t, http.StatusOK, movements.Status)
	require.Len(t, movements.dataList(), 3, "two receipts and one issue")
	for _, row := range movements.dataList() {
		m := row.(map[string]any)
		before := num(t, m, "quantity_before")
		delta := num(t, m, "quantity_delta")
		after := num(t, m, "quantity_after")
		assert.Equal(t, before+delta, after, "ledger arithmetic must balance: %v", m)
	}
}

// TestStockCannotGoNegative proves the ledger refuses to issue more than is on
// hand, which is the invariant the whole system rests on.
func TestStockCannotGoNegative(t *testing.T) {
	h := newHarness(t)
	admin := h.register("Negative Co", uniqueEmail("admin"))

	warehouseID := h.id(h.do(http.MethodPost, "/api/v1/warehouses", admin, map[string]any{
		"code": "W1", "name": "Depot", "is_default": true,
	}))
	productID := h.id(h.do(http.MethodPost, "/api/v1/products", admin, map[string]any{
		"sku": "P1", "name": "Thing", "cost_price": 5, "sell_price": 9,
	}))

	// Put 10 on hand.
	in := h.do(http.MethodPost, "/api/v1/stock/adjust", admin, map[string]any{
		"product_id": productID, "warehouse_id": warehouseID,
		"quantity": 10, "reason": "opening balance",
	})
	require.Equal(t, http.StatusOK, in.Status, "%v", in.Body)

	// Ask for 11 out.
	out := h.do(http.MethodPost, "/api/v1/stock/adjust", admin, map[string]any{
		"product_id": productID, "warehouse_id": warehouseID,
		"quantity": -11, "reason": "over-issue attempt",
	})
	assert.Equal(t, http.StatusConflict, out.Status, "%v", out.Body)
	assert.Equal(t, "INSUFFICIENT_STOCK", out.errorCode())

	// The balance must be untouched by the rejected attempt.
	items := h.do(http.MethodGet, "/api/v1/stock/items?filter[product_id]="+productID, admin, nil)
	require.Len(t, items.dataList(), 1)
	assert.Equal(t, 10.0, num(t, items.dataList()[0].(map[string]any), "quantity"))
}

// TestOfflineSyncIsIdempotent covers the mobile contract: a queued scan that is
// retried after a network failure must not double-count stock.
func TestOfflineSyncIsIdempotent(t *testing.T) {
	h := newHarness(t)
	admin := h.register("Offline Co", uniqueEmail("admin"))

	warehouseID := h.id(h.do(http.MethodPost, "/api/v1/warehouses", admin, map[string]any{
		"code": "W1", "name": "Depot", "is_default": true,
	}))
	productID := h.id(h.do(http.MethodPost, "/api/v1/products", admin, map[string]any{
		"sku": "P1", "name": "Thing", "cost_price": 5, "sell_price": 9,
	}))

	clientRequestID := uuid.NewString()
	movement := map[string]any{
		"product_id": productID, "warehouse_id": warehouseID,
		"quantity": 7, "reason": "scanned on the floor",
		"client_request_id": clientRequestID,
	}

	first := h.do(http.MethodPost, "/api/v1/stock/sync", admin,
		map[string]any{"movements": []map[string]any{movement}})
	require.Equal(t, http.StatusOK, first.Status, "%v", first.Body)
	assert.Equal(t, 1.0, num(t, first.data(), "applied"))
	assert.Equal(t, 0.0, num(t, first.data(), "duplicate"))

	// The device never saw the first response and retries the same entry.
	second := h.do(http.MethodPost, "/api/v1/stock/sync", admin,
		map[string]any{"movements": []map[string]any{movement}})
	require.Equal(t, http.StatusOK, second.Status, "%v", second.Body)
	assert.Equal(t, 0.0, num(t, second.data(), "applied"))
	assert.Equal(t, 1.0, num(t, second.data(), "duplicate"),
		"a replayed client_request_id must be recognised, not re-applied")

	items := h.do(http.MethodGet, "/api/v1/stock/items?filter[product_id]="+productID, admin, nil)
	require.Len(t, items.dataList(), 1)
	assert.Equal(t, 7.0, num(t, items.dataList()[0].(map[string]any), "quantity"),
		"stock must reflect one application, not two")
}

// TestSyncPartialFailure proves one bad entry does not discard the rest of the
// queue — the device needs to know exactly which entries to keep.
func TestSyncPartialFailure(t *testing.T) {
	h := newHarness(t)
	admin := h.register("Partial Co", uniqueEmail("admin"))

	warehouseID := h.id(h.do(http.MethodPost, "/api/v1/warehouses", admin, map[string]any{
		"code": "W1", "name": "Depot", "is_default": true,
	}))
	productID := h.id(h.do(http.MethodPost, "/api/v1/products", admin, map[string]any{
		"sku": "P1", "name": "Thing", "cost_price": 5, "sell_price": 9,
	}))

	resp := h.do(http.MethodPost, "/api/v1/stock/sync", admin, map[string]any{
		"movements": []map[string]any{
			{
				"product_id": productID, "warehouse_id": warehouseID,
				"quantity": 5, "reason": "good entry", "client_request_id": uuid.NewString(),
			},
			{
				// Removing stock that is not there must fail on its own.
				"product_id": productID, "warehouse_id": warehouseID,
				"quantity": -999, "reason": "bad entry", "client_request_id": uuid.NewString(),
			},
			{
				// No idempotency key: refused, because a retry could double-apply.
				"product_id": productID, "warehouse_id": warehouseID,
				"quantity": 3, "reason": "no key",
			},
		},
	})
	require.Equal(t, http.StatusOK, resp.Status, "%v", resp.Body)
	assert.Equal(t, 1.0, num(t, resp.data(), "applied"))
	assert.Equal(t, 2.0, num(t, resp.data(), "failed"))

	outcomes := resp.data()["outcomes"].([]any)
	require.Len(t, outcomes, 3)
	assert.Equal(t, "applied", outcomes[0].(map[string]any)["status"])
	assert.Equal(t, "failed", outcomes[1].(map[string]any)["status"])
	assert.Equal(t, "failed", outcomes[2].(map[string]any)["status"])

	items := h.do(http.MethodGet, "/api/v1/stock/items?filter[product_id]="+productID, admin, nil)
	require.Len(t, items.dataList(), 1)
	assert.Equal(t, 5.0, num(t, items.dataList()[0].(map[string]any), "quantity"),
		"only the good entry should have landed")
}

// TestConcurrentAdjustmentsStayConsistent hammers one stock coordinate from
// several goroutines. Without the row lock in Ledger.Apply, the read-modify-write
// would lose updates and the final balance would come out short.
func TestConcurrentAdjustmentsStayConsistent(t *testing.T) {
	h := newHarness(t)
	admin := h.register("Concurrent Co", uniqueEmail("admin"))

	warehouseID := h.id(h.do(http.MethodPost, "/api/v1/warehouses", admin, map[string]any{
		"code": "W1", "name": "Depot", "is_default": true,
	}))
	productID := h.id(h.do(http.MethodPost, "/api/v1/products", admin, map[string]any{
		"sku": "P1", "name": "Thing", "cost_price": 5, "sell_price": 9,
	}))

	const workers = 12
	var wg sync.WaitGroup
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func(n int) {
			defer wg.Done()
			h.do(http.MethodPost, "/api/v1/stock/adjust", admin, map[string]any{
				"product_id": productID, "warehouse_id": warehouseID,
				"quantity": 1, "reason": fmt.Sprintf("concurrent %d", n),
			})
		}(i)
	}
	wg.Wait()

	items := h.do(http.MethodGet, "/api/v1/stock/items?filter[product_id]="+productID, admin, nil)
	require.Len(t, items.dataList(), 1)
	assert.Equal(t, float64(workers), num(t, items.dataList()[0].(map[string]any), "quantity"),
		"every concurrent increment must be accounted for")
}

// TestRBACIsEnforcedPerRole checks the permission matrix over real HTTP, because
// a mismatch between the matrix and the route guards is invisible to unit tests.
func TestRBACIsEnforcedPerRole(t *testing.T) {
	h := newHarness(t)
	admin := h.register("RBAC Co", uniqueEmail("admin"))

	manager := h.addUser(admin, "manager", uniqueEmail("manager"))
	staff := h.addUser(admin, "warehouse_staff", uniqueEmail("staff"))
	viewer := h.addUser(admin, "viewer", uniqueEmail("viewer"))

	warehouseID := h.id(h.do(http.MethodPost, "/api/v1/warehouses", admin, map[string]any{
		"code": "W1", "name": "Depot", "is_default": true,
	}))
	productID := h.id(h.do(http.MethodPost, "/api/v1/products", admin, map[string]any{
		"sku": "P1", "name": "Thing", "cost_price": 5, "sell_price": 9,
	}))

	adjust := func(token string) int {
		return h.do(http.MethodPost, "/api/v1/stock/adjust", token, map[string]any{
			"product_id": productID, "warehouse_id": warehouseID,
			"quantity": 1, "reason": "rbac probe",
		}).Status
	}

	assert.Equal(t, http.StatusOK, adjust(staff), "warehouse staff move stock")
	assert.Equal(t, http.StatusOK, adjust(manager), "managers move stock")
	assert.Equal(t, http.StatusForbidden, adjust(viewer), "viewers are read-only")

	// Viewers can still read.
	assert.Equal(t, http.StatusOK,
		h.do(http.MethodGet, "/api/v1/stock/items", viewer, nil).Status)

	// User management is admin-only.
	assert.Equal(t, http.StatusForbidden,
		h.do(http.MethodGet, "/api/v1/users", manager, nil).Status)
	assert.Equal(t, http.StatusOK,
		h.do(http.MethodGet, "/api/v1/users", admin, nil).Status)

	// Catalog writes are closed to floor staff.
	assert.Equal(t, http.StatusForbidden,
		h.do(http.MethodPost, "/api/v1/products", staff, map[string]any{
			"sku": "P2", "name": "Another", "cost_price": 1, "sell_price": 2,
		}).Status)

	// No token at all is a 401, not a 403.
	assert.Equal(t, http.StatusUnauthorized,
		h.do(http.MethodGet, "/api/v1/stock/items", "", nil).Status)
}

// TestTenantIsolation proves one organization cannot read or touch another's
// data, which is the guarantee the multi-tenant schema exists to provide.
func TestTenantIsolation(t *testing.T) {
	h := newHarness(t)

	tenantA := h.register("Tenant A", uniqueEmail("a-admin"))
	warehouseA := h.id(h.do(http.MethodPost, "/api/v1/warehouses", tenantA, map[string]any{
		"code": "WA", "name": "A Depot", "is_default": true,
	}))
	productA := h.id(h.do(http.MethodPost, "/api/v1/products", tenantA, map[string]any{
		"sku": "A-SKU", "name": "A Product", "barcode": "9990000000001",
		"cost_price": 5, "sell_price": 9,
	}))
	require.Equal(t, http.StatusOK, h.do(http.MethodPost, "/api/v1/stock/adjust", tenantA, map[string]any{
		"product_id": productA, "warehouse_id": warehouseA,
		"quantity": 50, "reason": "opening",
	}).Status)

	tenantB := h.register("Tenant B", uniqueEmail("b-admin"))

	// B's lists must be empty despite A's data existing.
	assert.Empty(t, h.do(http.MethodGet, "/api/v1/products", tenantB, nil).dataList())
	assert.Empty(t, h.do(http.MethodGet, "/api/v1/stock/items", tenantB, nil).dataList())
	assert.Empty(t, h.do(http.MethodGet, "/api/v1/warehouses", tenantB, nil).dataList())

	// Direct id access must 404, not 403 — B should not learn the id exists.
	assert.Equal(t, http.StatusNotFound,
		h.do(http.MethodGet, "/api/v1/products/"+productA, tenantB, nil).Status)

	// A's barcode must not resolve for B.
	assert.Equal(t, http.StatusNotFound,
		h.do(http.MethodGet, "/api/v1/products/scan/9990000000001", tenantB, nil).Status)

	// B cannot move A's stock.
	cross := h.do(http.MethodPost, "/api/v1/stock/adjust", tenantB, map[string]any{
		"product_id": productA, "warehouse_id": warehouseA,
		"quantity": -10, "reason": "cross-tenant attempt",
	})
	assert.NotEqual(t, http.StatusOK, cross.Status, "cross-tenant stock movement must fail")

	// A's stock is untouched.
	items := h.do(http.MethodGet, "/api/v1/stock/items", tenantA, nil)
	require.Len(t, items.dataList(), 1)
	assert.Equal(t, 50.0, num(t, items.dataList()[0].(map[string]any), "quantity"))
}

// TestAuthLifecycle covers login, refresh rotation and refresh-replay defence.
func TestAuthLifecycle(t *testing.T) {
	h := newHarness(t)

	email := uniqueEmail("lifecycle")
	register := h.do(http.MethodPost, "/api/v1/auth/register", "", map[string]any{
		"organization_name": "Lifecycle Co", "full_name": "Life Cycle",
		"email": email, "password": "SuperSecret123!",
	})
	require.Equal(t, http.StatusCreated, register.Status, "%v", register.Body)
	tokens := register.data()["tokens"].(map[string]any)
	refresh := tokens["refresh_token"].(string)

	// Wrong password must not reveal whether the account exists.
	bad := h.do(http.MethodPost, "/api/v1/auth/login", "", map[string]any{
		"email": email, "password": "WrongPassword1!",
	})
	assert.Equal(t, http.StatusUnauthorized, bad.Status)
	unknown := h.do(http.MethodPost, "/api/v1/auth/login", "", map[string]any{
		"email": uniqueEmail("nobody"), "password": "WrongPassword1!",
	})
	assert.Equal(t, http.StatusUnauthorized, unknown.Status)
	assert.Equal(t, bad.Body["error"].(map[string]any)["message"],
		unknown.Body["error"].(map[string]any)["message"],
		"a bad password and an unknown email must be indistinguishable")

	// Rotate the refresh token.
	rotated := h.do(http.MethodPost, "/api/v1/auth/refresh", "", map[string]any{
		"refresh_token": refresh,
	})
	require.Equal(t, http.StatusOK, rotated.Status, "%v", rotated.Body)
	newTokens := rotated.data()["tokens"].(map[string]any)
	assert.NotEqual(t, refresh, newTokens["refresh_token"], "rotation must issue a fresh token")

	// Replaying the old token is treated as a leak: the session family dies.
	replay := h.do(http.MethodPost, "/api/v1/auth/refresh", "", map[string]any{
		"refresh_token": refresh,
	})
	assert.Equal(t, http.StatusUnauthorized, replay.Status, "%v", replay.Body)

	dead := h.do(http.MethodPost, "/api/v1/auth/refresh", "", map[string]any{
		"refresh_token": newTokens["refresh_token"],
	})
	assert.Equal(t, http.StatusUnauthorized, dead.Status,
		"detecting a replay must revoke the whole family, including the rotated token")

	// /auth/me reports the permission set the clients build navigation from.
	me := h.do(http.MethodGet, "/api/v1/auth/me", tokens["access_token"].(string), nil)
	require.Equal(t, http.StatusOK, me.Status, "%v", me.Body)
	assert.Equal(t, email, me.data()["user"].(map[string]any)["email"])
	assert.NotEmpty(t, me.data()["permissions"])
}

// TestTransferMovesStockBetweenWarehouses covers the two-phase transfer,
// including the in-transit window where stock belongs to neither site.
func TestTransferMovesStockBetweenWarehouses(t *testing.T) {
	h := newHarness(t)
	admin := h.register("Transfer Co", uniqueEmail("admin"))

	fromID := h.id(h.do(http.MethodPost, "/api/v1/warehouses", admin, map[string]any{
		"code": "FROM", "name": "Source", "is_default": true,
	}))
	toID := h.id(h.do(http.MethodPost, "/api/v1/warehouses", admin, map[string]any{
		"code": "TO", "name": "Destination",
	}))
	productID := h.id(h.do(http.MethodPost, "/api/v1/products", admin, map[string]any{
		"sku": "P1", "name": "Thing", "cost_price": 5, "sell_price": 9,
	}))

	require.Equal(t, http.StatusOK, h.do(http.MethodPost, "/api/v1/stock/adjust", admin, map[string]any{
		"product_id": productID, "warehouse_id": fromID, "quantity": 40, "reason": "opening",
	}).Status)

	transfer := h.do(http.MethodPost, "/api/v1/stock/transfers", admin, map[string]any{
		"from_warehouse_id": fromID, "to_warehouse_id": toID,
		"lines": []map[string]any{{"product_id": productID, "quantity": 15}},
	})
	require.Equal(t, http.StatusCreated, transfer.Status, "%v", transfer.Body)
	transferID := h.id(transfer)

	// A same-warehouse transfer is meaningless and must be refused.
	same := h.do(http.MethodPost, "/api/v1/stock/transfers", admin, map[string]any{
		"from_warehouse_id": fromID, "to_warehouse_id": fromID,
		"lines": []map[string]any{{"product_id": productID, "quantity": 1}},
	})
	assert.Equal(t, http.StatusBadRequest, same.Status)

	// Receiving before dispatch must fail.
	assert.Equal(t, http.StatusConflict,
		h.do(http.MethodPost, "/api/v1/stock/transfers/"+transferID+"/receive", admin, nil).Status)

	require.Equal(t, http.StatusOK,
		h.do(http.MethodPost, "/api/v1/stock/transfers/"+transferID+"/dispatch", admin, nil).Status)

	// In transit: source is down 15, destination has not received yet.
	assert.Equal(t, 25.0, totalFor(t, h, admin, productID, fromID))
	assert.Equal(t, 0.0, totalFor(t, h, admin, productID, toID))

	require.Equal(t, http.StatusOK,
		h.do(http.MethodPost, "/api/v1/stock/transfers/"+transferID+"/receive", admin, nil).Status)

	assert.Equal(t, 25.0, totalFor(t, h, admin, productID, fromID))
	assert.Equal(t, 15.0, totalFor(t, h, admin, productID, toID))
}

// TestBatchTrackedProductRequiresABatch covers the expiry-tracking guarantee:
// batch-tracked stock can never be recorded without a lot.
func TestBatchTrackedProductRequiresABatch(t *testing.T) {
	h := newHarness(t)
	admin := h.register("Batch Co", uniqueEmail("admin"))

	warehouseID := h.id(h.do(http.MethodPost, "/api/v1/warehouses", admin, map[string]any{
		"code": "W1", "name": "Depot", "is_default": true,
	}))
	productID := h.id(h.do(http.MethodPost, "/api/v1/products", admin, map[string]any{
		"sku": "MILK-1", "name": "Milk", "cost_price": 1, "sell_price": 2,
		"track_batches": true, "shelf_life_days": 14,
	}))

	// Without a batch: refused.
	noBatch := h.do(http.MethodPost, "/api/v1/stock/adjust", admin, map[string]any{
		"product_id": productID, "warehouse_id": warehouseID,
		"quantity": 10, "reason": "receipt without a lot",
	})
	assert.Equal(t, http.StatusBadRequest, noBatch.Status, "%v", noBatch.Body)

	batchID := h.id(h.do(http.MethodPost, "/api/v1/stock/batches", admin, map[string]any{
		"product_id": productID, "lot_number": "LOT-1", "expiry_date": "2027-01-01T00:00:00Z",
	}))

	withBatch := h.do(http.MethodPost, "/api/v1/stock/adjust", admin, map[string]any{
		"product_id": productID, "warehouse_id": warehouseID, "batch_id": batchID,
		"quantity": 10, "reason": "receipt with a lot",
	})
	assert.Equal(t, http.StatusOK, withBatch.Status, "%v", withBatch.Body)
}

// TestCycleCountPostsVariance covers the stock-take flow through to the ledger.
func TestCycleCountPostsVariance(t *testing.T) {
	h := newHarness(t)
	admin := h.register("Count Co", uniqueEmail("admin"))

	warehouseID := h.id(h.do(http.MethodPost, "/api/v1/warehouses", admin, map[string]any{
		"code": "W1", "name": "Depot", "is_default": true,
	}))
	productID := h.id(h.do(http.MethodPost, "/api/v1/products", admin, map[string]any{
		"sku": "P1", "name": "Thing", "cost_price": 5, "sell_price": 9,
	}))
	require.Equal(t, http.StatusOK, h.do(http.MethodPost, "/api/v1/stock/adjust", admin, map[string]any{
		"product_id": productID, "warehouse_id": warehouseID, "quantity": 100, "reason": "opening",
	}).Status)

	count := h.do(http.MethodPost, "/api/v1/stock/cycle-counts", admin, map[string]any{
		"warehouse_id": warehouseID,
	})
	require.Equal(t, http.StatusCreated, count.Status, "%v", count.Body)
	countID := h.id(count)

	countLines := count.data()["lines"].([]any)
	require.Len(t, countLines, 1)
	lineID := countLines[0].(map[string]any)["id"].(string)
	assert.Equal(t, 100.0, num(t, countLines[0].(map[string]any), "expected_quantity"))

	// The shelf actually holds 96: four units of shrinkage.
	record := h.do(http.MethodPost, "/api/v1/stock/cycle-counts/"+countID+"/record", admin, map[string]any{
		"lines": []map[string]any{{"line_id": lineID, "counted_quantity": 96, "note": "shrinkage"}},
	})
	require.Equal(t, http.StatusOK, record.Status, "%v", record.Body)

	complete := h.do(http.MethodPost, "/api/v1/stock/cycle-counts/"+countID+"/complete", admin, nil)
	require.Equal(t, http.StatusOK, complete.Status, "%v", complete.Body)
	assert.Equal(t, "completed", complete.data()["status"])

	assert.Equal(t, 96.0, totalFor(t, h, admin, productID, warehouseID),
		"the counted quantity becomes the truth")

	// The correction must be traceable as a `count` movement.
	movements := h.do(http.MethodGet,
		"/api/v1/stock/movements?filter[product_id]="+productID+"&filter[type]=count", admin, nil)
	require.Len(t, movements.dataList(), 1)
	assert.Equal(t, -4.0, num(t, movements.dataList()[0].(map[string]any), "quantity_delta"))
}

// TestValidationErrorsNameTheirFields checks the error envelope shape the web
// forms depend on.
func TestValidationErrorsNameTheirFields(t *testing.T) {
	h := newHarness(t)

	resp := h.do(http.MethodPost, "/api/v1/auth/register", "", map[string]any{
		"organization_name": "X", // too short
		"full_name":         "",  // required
		"email":             "not-an-email",
		"password":          "short",
	})
	require.Equal(t, http.StatusBadRequest, resp.Status)
	assert.Equal(t, "VALIDATION_ERROR", resp.errorCode())

	details := resp.Body["error"].(map[string]any)["details"].(map[string]any)
	assert.Contains(t, details, "email")
	assert.Contains(t, details, "password")
	assert.Contains(t, details, "full_name")
}

// TestHealthEndpointsReportDegradedCache confirms the API stays ready when
// Redis is unreachable, which is what the uptime target requires.
func TestHealthEndpointsReportDegradedCache(t *testing.T) {
	h := newHarness(t)

	live := h.do(http.MethodGet, "/health/live", "", nil)
	assert.Equal(t, http.StatusOK, live.Status)

	ready := h.do(http.MethodGet, "/health/ready", "", nil)
	// The harness runs without Redis on purpose.
	assert.Equal(t, http.StatusOK, ready.Status, "a Redis outage must not fail readiness")
	assert.Equal(t, "degraded", ready.Body["status"])

	checks := ready.Body["checks"].(map[string]any)
	assert.Equal(t, "ok", checks["postgres"])
	assert.Equal(t, "unreachable", checks["redis"])
}

// totalFor sums on-hand quantity for a product in one warehouse.
func totalFor(t *testing.T, h *harness, token, productID, warehouseID string) float64 {
	t.Helper()

	resp := h.do(http.MethodGet,
		"/api/v1/stock/items?filter[product_id]="+productID+"&filter[warehouse_id]="+warehouseID,
		token, nil)
	require.Equal(t, http.StatusOK, resp.Status, "%v", resp.Body)

	total := 0.0
	for _, row := range resp.dataList() {
		total += num(t, row.(map[string]any), "quantity")
	}
	return total
}
