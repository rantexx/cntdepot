package auth

import "depot/internal/tenancy"

// Perm is what a route needs from the user's role in the current company.
type Perm string

const (
	PermGate   Perm = "gate"
	PermOffice Perm = "office"
	PermManage Perm = "manage"
	PermOwner  Perm = "owner"
)

// Roles and permissions are ranked: a role holds every permission up to its rank.
var (
	roleRank = map[tenancy.Role]int{tenancy.Gate: 1, tenancy.Office: 2, tenancy.Manager: 3, tenancy.Owner: 4}
	permRank = map[Perm]int{PermGate: 1, PermOffice: 2, PermManage: 3, PermOwner: 4}
)

// Can reports whether role r has permission p. Unknown roles and perms have none.
func Can(r tenancy.Role, p Perm) bool {
	need, ok := permRank[p]
	return ok && roleRank[r] >= need
}
