package clients

import (
	"context"

	"connectrpc.com/connect"
)

// tokenCtxKey carries the bearer token to forward on a downstream call. It is set
// by WithToken and read by the forwarding interceptor.
type tokenCtxKey struct{}

// WithToken returns a context that, when used for a downstream Connect call, makes
// the ForwardAuth interceptor attach `Authorization: Bearer <token>`. BFF handlers
// call this with the verified inbound token so backends re-verify and apply RLS.
func WithToken(ctx context.Context, token string) context.Context {
	return context.WithValue(ctx, tokenCtxKey{}, token)
}

// ForwardAuth is a Connect client interceptor that copies the token placed on the
// context by WithToken into the outgoing Authorization header. Public BFF calls
// (start-otp, verify-otp, customer-session) simply don't set a token, so nothing
// is forwarded.
func ForwardAuth() connect.Interceptor {
	return connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			if tok, ok := ctx.Value(tokenCtxKey{}).(string); ok && tok != "" {
				req.Header().Set("Authorization", "Bearer "+tok)
			}
			return next(ctx, req)
		}
	})
}
