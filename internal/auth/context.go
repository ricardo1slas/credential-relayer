package auth

import "context"

type contextKey struct{}

func withKeyName(ctx context.Context, name string) context.Context {
	return context.WithValue(ctx, contextKey{}, name)
}

// KeyName extracts the authenticated API key name from the context.
// Returns empty string if not authenticated.
func KeyName(ctx context.Context) string {
	name, _ := ctx.Value(contextKey{}).(string)
	return name
}
