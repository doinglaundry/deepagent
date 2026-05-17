// Package runtime carries cross-cutting per-request state on context.Context.
package runtime

import "context"

type userIDKey struct{}

// DefaultUserID is the synthetic uid used by the CLI / when no X-User-Id header was set.
const DefaultUserID = "local"

// WithUserID returns a ctx carrying uid.
func WithUserID(ctx context.Context, uid string) context.Context {
	return context.WithValue(ctx, userIDKey{}, uid)
}

// GetEffectiveUserID returns the uid on ctx, or DefaultUserID when missing/empty.
func GetEffectiveUserID(ctx context.Context) string {
	if v, ok := ctx.Value(userIDKey{}).(string); ok && v != "" {
		return v
	}
	return DefaultUserID
}
