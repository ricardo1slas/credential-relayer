package auth

import "context"

type contextKey struct{}

// Principal is the authenticated caller: the API key name and the targets it may use.
type Principal struct {
	Name    string
	targets map[string]struct{}
	all     bool
}

// newPrincipal: an empty list or "*" means every target (backward compatible; main.go warns).
func newPrincipal(name string, targets []string) *Principal {
	p := &Principal{Name: name, targets: make(map[string]struct{}, len(targets)), all: len(targets) == 0}
	for _, t := range targets {
		if t == "*" {
			p.all = true
		}
		p.targets[t] = struct{}{}
	}
	return p
}

// WithKey attaches a principal to ctx. The auth middleware uses it; tests can too.
func WithKey(ctx context.Context, name string, targets []string) context.Context {
	return context.WithValue(ctx, contextKey{}, newPrincipal(name, targets))
}

// KeyName extracts the authenticated API key name from the context.
// Returns empty string if not authenticated.
func KeyName(ctx context.Context) string {
	p, _ := ctx.Value(contextKey{}).(*Principal)
	if p == nil {
		return ""
	}
	return p.Name
}

// TargetAllowed reports whether the authenticated key may use target.
// Fails closed: no principal in ctx (route missing the auth middleware) means denied.
func TargetAllowed(ctx context.Context, target string) bool {
	p, _ := ctx.Value(contextKey{}).(*Principal)
	if p == nil {
		return false
	}
	if p.all {
		return true
	}
	_, ok := p.targets[target]
	return ok
}
