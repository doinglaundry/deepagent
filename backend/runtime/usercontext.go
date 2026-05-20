// Package runtime carries cross-cutting per-request state on context.Context.
package runtime

import (
	"context"

	"eino-cli/backend/consts"
)

type userIDKey struct{}

func WithUserID(ctx context.Context, uid string) context.Context {
	return context.WithValue(ctx, userIDKey{}, uid)
}

func GetEffectiveUserID(ctx context.Context) string {
	if v, ok := ctx.Value(userIDKey{}).(string); ok && v != "" {
		return v
	}
	return consts.DefaultUserID
}
