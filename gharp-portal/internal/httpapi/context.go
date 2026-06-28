package httpapi

import "context"

type ctxKey int

const (
	ctxKeyUser ctxKey = iota
	ctxKeyCSRF
)

// SetUserInContext stores a User in the request context.
// WS-B's RequireUser/RequireAdmin middleware must call this so handlers can retrieve it.
func SetUserInContext(ctx context.Context, u User) context.Context {
	return context.WithValue(ctx, ctxKeyUser, u)
}

// UserFromContext retrieves the User set by RequireUser/RequireAdmin middleware.
func UserFromContext(ctx context.Context) (User, bool) {
	u, ok := ctx.Value(ctxKeyUser).(User)
	return u, ok
}

// SetCSRFInContext stores the CSRF token in the request context.
// WS-B's middleware must call this after reading the session so requireCSRF can validate it.
func SetCSRFInContext(ctx context.Context, csrf string) context.Context {
	return context.WithValue(ctx, ctxKeyCSRF, csrf)
}

// CSRFFromContext retrieves the CSRF token stored by WS-B middleware.
func CSRFFromContext(ctx context.Context) string {
	s, _ := ctx.Value(ctxKeyCSRF).(string)
	return s
}
