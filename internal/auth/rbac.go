package auth

import (
	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"

	"github.com/sabin-bhattarai/ims-backend/internal/shared"
)

// Role is the RBAC role enum. The same four values are mirrored in ims-web
// (src/lib/rbac.ts) and ims-mobile (lib/core/auth/roles.dart); all three are
// generated from the OpenAPI enum, so adding a role is a contract change.
type Role string

const (
	RoleAdmin          Role = "admin"
	RoleManager        Role = "manager"
	RoleWarehouseStaff Role = "warehouse_staff"
	RoleViewer         Role = "viewer"
)

// Valid reports whether r is a known role.
func (r Role) Valid() bool {
	switch r {
	case RoleAdmin, RoleManager, RoleWarehouseStaff, RoleViewer:
		return true
	}
	return false
}

// rank orders roles for privilege comparisons. Higher is more privileged.
func (r Role) rank() int {
	switch r {
	case RoleAdmin:
		return 4
	case RoleManager:
		return 3
	case RoleWarehouseStaff:
		return 2
	case RoleViewer:
		return 1
	}
	return 0
}

// AtLeast reports whether r is at least as privileged as other.
func (r Role) AtLeast(other Role) bool { return r.rank() >= other.rank() }

// Permission is a fine-grained capability. Handlers are guarded by permission
// rather than by role so that the role→permission mapping can change in one
// place.
type Permission string

const (
	PermUserManage      Permission = "user:manage"
	PermProductRead     Permission = "product:read"
	PermProductWrite    Permission = "product:write"
	PermWarehouseRead   Permission = "warehouse:read"
	PermWarehouseWrite  Permission = "warehouse:write"
	PermStockRead       Permission = "stock:read"
	PermStockAdjust     Permission = "stock:adjust"
	PermStockTransfer   Permission = "stock:transfer"
	PermStockCount      Permission = "stock:count"
	PermPurchaseRead    Permission = "purchase:read"
	PermPurchaseWrite   Permission = "purchase:write"
	PermPurchaseApprove Permission = "purchase:approve"
	PermSalesRead       Permission = "sales:read"
	PermSalesWrite      Permission = "sales:write"
	PermSalesShip       Permission = "sales:ship"
	PermReturnApprove   Permission = "return:approve"
	PermReportRead      Permission = "report:read"
	PermAuditRead       Permission = "audit:read"
)

// rolePermissions is the authoritative role→permission matrix.
//
// The shape of it matters more than the detail: warehouse staff can move
// physical stock but cannot approve money (PO approval, returns); managers can
// approve spend but cannot manage users; viewers are strictly read-only.
var rolePermissions = map[Role]map[Permission]bool{
	RoleAdmin: nil, // nil means "everything" — see Can.
	RoleManager: {
		PermProductRead: true, PermProductWrite: true,
		PermWarehouseRead: true, PermWarehouseWrite: true,
		PermStockRead: true, PermStockAdjust: true, PermStockTransfer: true, PermStockCount: true,
		PermPurchaseRead: true, PermPurchaseWrite: true, PermPurchaseApprove: true,
		PermSalesRead: true, PermSalesWrite: true, PermSalesShip: true,
		PermReturnApprove: true,
		PermReportRead:    true, PermAuditRead: true,
	},
	RoleWarehouseStaff: {
		PermProductRead:   true,
		PermWarehouseRead: true,
		PermStockRead:     true, PermStockAdjust: true, PermStockTransfer: true, PermStockCount: true,
		PermPurchaseRead: true, // needs to see the PO being received against
		PermSalesRead:    true, PermSalesShip: true,
		PermReportRead: true,
	},
	RoleViewer: {
		PermProductRead: true, PermWarehouseRead: true, PermStockRead: true,
		PermPurchaseRead: true, PermSalesRead: true, PermReportRead: true,
	},
}

// Can reports whether a role holds a permission.
func Can(role Role, perm Permission) bool {
	if role == RoleAdmin {
		return true
	}
	perms, ok := rolePermissions[role]
	if !ok {
		return false
	}
	return perms[perm]
}

// PermissionsFor returns every permission held by a role, for the
// /auth/me payload the clients use to build their navigation.
func PermissionsFor(role Role) []Permission {
	if role == RoleAdmin {
		all := make([]Permission, 0, len(allPermissions))
		return append(all, allPermissions...)
	}
	perms := make([]Permission, 0, len(rolePermissions[role]))
	for _, p := range allPermissions {
		if rolePermissions[role][p] {
			perms = append(perms, p)
		}
	}
	return perms
}

// allPermissions is kept in a fixed order so /auth/me responses are stable and
// cacheable.
var allPermissions = []Permission{
	PermUserManage,
	PermProductRead, PermProductWrite,
	PermWarehouseRead, PermWarehouseWrite,
	PermStockRead, PermStockAdjust, PermStockTransfer, PermStockCount,
	PermPurchaseRead, PermPurchaseWrite, PermPurchaseApprove,
	PermSalesRead, PermSalesWrite, PermSalesShip,
	PermReturnApprove,
	PermReportRead, PermAuditRead,
}

// Identity is the authenticated caller, derived from the access token and
// stored on the request context.
type Identity struct {
	UserID    uuid.UUID `json:"user_id"`
	OrgID     uuid.UUID `json:"organization_id"`
	Email     string    `json:"email"`
	Role      Role      `json:"role"`
	SessionID string    `json:"session_id"`
}

// Can reports whether the caller holds a permission.
func (i Identity) Can(perm Permission) bool { return Can(i.Role, perm) }

// identityKey is the Fiber locals key holding the Identity.
const identityKey = "ims.identity"

// StoreIdentity attaches the caller to the request context.
func StoreIdentity(c *fiber.Ctx, id Identity) { c.Locals(identityKey, id) }

// FromContext returns the authenticated caller. It returns an error rather
// than a zero Identity so a handler mounted without the auth middleware fails
// loudly instead of silently querying organization_id = uuid.Nil.
func FromContext(c *fiber.Ctx) (Identity, error) {
	id, ok := c.Locals(identityKey).(Identity)
	if !ok || id.UserID == uuid.Nil {
		return Identity{}, shared.Unauthorized("authentication required")
	}
	return id, nil
}

// MustIdentity is FromContext for handlers already behind RequireAuth.
func MustIdentity(c *fiber.Ctx) Identity {
	id, _ := c.Locals(identityKey).(Identity)
	return id
}
