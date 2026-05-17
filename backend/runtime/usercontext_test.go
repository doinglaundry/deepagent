package runtime

import (
	"context"
	"testing"
)

func TestUserContext(t *testing.T) {
	t.Run("missing key falls back to local", func(t *testing.T) {
		if got := GetEffectiveUserID(context.Background()); got != DefaultUserID {
			t.Errorf("got %q, want %q", got, DefaultUserID)
		}
	})

	t.Run("empty string also falls back", func(t *testing.T) {
		ctx := WithUserID(context.Background(), "")
		if got := GetEffectiveUserID(ctx); got != DefaultUserID {
			t.Errorf("got %q, want %q", got, DefaultUserID)
		}
	})

	t.Run("set value comes back", func(t *testing.T) {
		ctx := WithUserID(context.Background(), "alice")
		if got := GetEffectiveUserID(ctx); got != "alice" {
			t.Errorf("got %q, want alice", got)
		}
	})

	t.Run("inner overrides outer", func(t *testing.T) {
		ctx := WithUserID(WithUserID(context.Background(), "alice"), "bob")
		if got := GetEffectiveUserID(ctx); got != "bob" {
			t.Errorf("got %q, want bob", got)
		}
	})
}
