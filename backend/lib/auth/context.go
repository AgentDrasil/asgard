package auth

import (
	"context"
)

type userContextKey struct{}

// User represents the authenticated identity and roles extracted from a verified token.
type User struct {
	Subject string
	Roles   []string
}

// AdminRole is the default role required to access administrative/management APIs.
const AdminRole = "admin"

// WithUser returns a new Context that carries the authenticated User.
func WithUser(ctx context.Context, u *User) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if u == nil {
		return ctx
	}
	return context.WithValue(ctx, userContextKey{}, u)
}

// UserFromContext extracts the authenticated User from the context, if present.
func UserFromContext(ctx context.Context) (*User, bool) {
	if ctx == nil {
		return nil, false
	}
	u, ok := ctx.Value(userContextKey{}).(*User)
	return u, ok && u != nil
}

// HasRole reports whether the context contains an authenticated user who holds the given role.
func HasRole(ctx context.Context, role string) bool {
	u, ok := UserFromContext(ctx)
	if !ok || u == nil {
		return false
	}
	return containsRole(u.Roles, role)
}
