package service

import (
	"context"

	"github.com/reubenmiller/go-c8y/v2/pkg/c8y/api"
	"resty.dev/v3"
)

// A two-factor session's token has to travel with every call made on its behalf,
// including the one that verifies it: "if a request contains the header
// tfatoken, [it] always must be included in the request to /user/currentUser".
//
// The library has no way to add a header to a single request - api.WithAuth
// carries credentials and nothing else - so this adds the one seam it does
// offer: a request middleware on the exported resty client, reading a value the
// caller put on the context. Without it a two-factor session fails verification
// for the same reason a cookie session did before this change.

// tfaTokenKey is the context key the middleware reads.
type tfaTokenKey struct{}

// withTFAToken carries a two-factor token to the next request made with ctx.
func withTFAToken(ctx context.Context, token string) context.Context {
	return context.WithValue(ctx, tfaTokenKey{}, token)
}

// tfaTokenFrom is the token to send, if any.
func tfaTokenFrom(ctx context.Context) string {
	token, _ := ctx.Value(tfaTokenKey{}).(string)
	return token
}

// forwardTFAToken registers the middleware on a client.
//
// Registered on every client rather than only the one used for verification: it
// does nothing unless a context carries a token, and a client that behaves
// differently from its siblings is a thing to trip over later.
func forwardTFAToken(client *api.Client) {
	client.HTTPClient.AddRequestMiddleware(func(_ *resty.Client, r *resty.Request) error {
		if token := tfaTokenFrom(r.Context()); token != "" {
			r.SetHeader(headerTFA, token)
		}
		return nil
	})
}
