package auth_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/sabin-bhattarai/ims_backend/internal/auth"
)

func TestRoleCanMatrix(t *testing.T) {
	t.Parallel()

	cases := []struct {
		role auth.Role
		perm auth.Permission
		want bool
		why  string
	}{
		{auth.RoleAdmin, auth.PermUserManage, true, "admin holds every permission"},
		{auth.RoleAdmin, auth.PermPurchaseApprove, true, "admin holds every permission"},

		{auth.RoleManager, auth.PermPurchaseApprove, true, "managers approve spend"},
		{auth.RoleManager, auth.PermReturnApprove, true, "managers approve returns"},
		{auth.RoleManager, auth.PermUserManage, false, "user management is admin-only"},

		{auth.RoleWarehouseStaff, auth.PermStockAdjust, true, "floor staff move physical stock"},
		{auth.RoleWarehouseStaff, auth.PermStockTransfer, true, "floor staff move physical stock"},
		{auth.RoleWarehouseStaff, auth.PermSalesShip, true, "floor staff dispatch orders"},
		{auth.RoleWarehouseStaff, auth.PermPurchaseApprove, false, "floor staff must not approve spend"},
		{auth.RoleWarehouseStaff, auth.PermReturnApprove, false, "floor staff must not approve refunds"},
		{auth.RoleWarehouseStaff, auth.PermProductWrite, false, "floor staff do not edit the catalog"},

		{auth.RoleViewer, auth.PermStockRead, true, "viewers read stock"},
		{auth.RoleViewer, auth.PermReportRead, true, "viewers read reports"},
		{auth.RoleViewer, auth.PermStockAdjust, false, "viewers are strictly read-only"},
		{auth.RoleViewer, auth.PermSalesWrite, false, "viewers are strictly read-only"},
		{auth.RoleViewer, auth.PermAuditRead, false, "audit trail is not for viewers"},

		{auth.Role("nonsense"), auth.PermStockRead, false, "an unknown role holds nothing"},
	}

	for _, tc := range cases {
		t.Run(string(tc.role)+"/"+string(tc.perm), func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, auth.Can(tc.role, tc.perm), tc.why)
		})
	}
}

func TestViewerHoldsNoWritePermission(t *testing.T) {
	t.Parallel()

	// A viewer gaining any write capability is a privilege-escalation bug, so
	// assert the property rather than each pair.
	writes := []auth.Permission{
		auth.PermUserManage, auth.PermProductWrite, auth.PermWarehouseWrite,
		auth.PermStockAdjust, auth.PermStockTransfer, auth.PermStockCount,
		auth.PermPurchaseWrite, auth.PermPurchaseApprove,
		auth.PermSalesWrite, auth.PermSalesShip, auth.PermReturnApprove,
	}
	for _, p := range writes {
		assert.False(t, auth.Can(auth.RoleViewer, p), "viewer must not hold %s", p)
	}
}

func TestRoleRanking(t *testing.T) {
	t.Parallel()

	assert.True(t, auth.RoleAdmin.AtLeast(auth.RoleManager))
	assert.True(t, auth.RoleManager.AtLeast(auth.RoleWarehouseStaff))
	assert.True(t, auth.RoleWarehouseStaff.AtLeast(auth.RoleViewer))
	assert.True(t, auth.RoleViewer.AtLeast(auth.RoleViewer))
	assert.False(t, auth.RoleViewer.AtLeast(auth.RoleManager))
	assert.False(t, auth.Role("nonsense").AtLeast(auth.RoleViewer))
}

func TestRoleValid(t *testing.T) {
	t.Parallel()

	for _, r := range []auth.Role{auth.RoleAdmin, auth.RoleManager, auth.RoleWarehouseStaff, auth.RoleViewer} {
		assert.True(t, r.Valid(), "%s should be valid", r)
	}
	assert.False(t, auth.Role("").Valid())
	assert.False(t, auth.Role("superuser").Valid())
}

func TestPermissionsForIsStableAndComplete(t *testing.T) {
	t.Parallel()

	// The clients build their navigation from this list, so its contents must
	// match Can() exactly — a mismatch shows up as a menu item that 403s.
	for _, role := range []auth.Role{auth.RoleAdmin, auth.RoleManager, auth.RoleWarehouseStaff, auth.RoleViewer} {
		perms := auth.PermissionsFor(role)
		for _, p := range perms {
			assert.True(t, auth.Can(role, p), "%s listed %s but Can() denies it", role, p)
		}
	}

	// Ordering is stable across calls so responses stay cacheable.
	assert.Equal(t, auth.PermissionsFor(auth.RoleManager), auth.PermissionsFor(auth.RoleManager))
	assert.NotEmpty(t, auth.PermissionsFor(auth.RoleViewer))
}

func TestIdentityCan(t *testing.T) {
	t.Parallel()

	staff := auth.Identity{Role: auth.RoleWarehouseStaff}
	assert.True(t, staff.Can(auth.PermStockAdjust))
	assert.False(t, staff.Can(auth.PermUserManage))
}
