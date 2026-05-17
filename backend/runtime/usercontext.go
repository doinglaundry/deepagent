// Package runtime carries cross-cutting runtime state (per-request user id,
// later: thread id, sandbox id) on context.Context. Empty-struct ctx keys
// keep the value namespace package-private — no risk of iota collisions
// with other packages that pick the same int.
package runtime

import "context"

type userIDKey struct{}

// DefaultUserID is the synthetic uid used by the CLI / when no X-User-ID
// header reached the request — keeps single-user code paths working
// unchanged while multi-tenant mounts still get a deterministic dir.
const DefaultUserID = "local"

// WithUserID returns a ctx carrying uid. Empty uid is allowed (the lookup
// side falls back to DefaultUserID), so callers don't have to special-case.
func WithUserID(ctx context.Context, uid string) context.Context {
	return context.WithValue(ctx, userIDKey{}, uid)
}

// GetEffectiveUserID returns the uid stashed on ctx, or DefaultUserID
// when missing / empty. Use this — never read userIDKey directly.
func GetEffectiveUserID(ctx context.Context) string {
	if v, ok := ctx.Value(userIDKey{}).(string); ok && v != "" {
		return v
	}
	return DefaultUserID
}
