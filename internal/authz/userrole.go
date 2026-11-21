package authz

import (
	"github.com/gempir/agenthold/internal/config"
	"github.com/gempir/agenthold/internal/store/db"
)

// UserRole is u's role on the installation: an admin's override when one is
// set, else the role sign-in configuration gave it. The row is read on every
// request, so a change takes effect at the user's next request. Anything
// unreadable is Disabled.
func UserRole(u db.User) config.Role {
	if r, ok := config.ParseRole(u.RoleOverride); ok {
		return r
	}
	if r, ok := config.ParseRole(u.Role); ok {
		return r
	}
	return config.RoleDisabled
}

// ErrDisabledMessage is what a Disabled user is told.
const ErrDisabledMessage = "your account has no access to this agenthold; ask an admin to give it a role"
