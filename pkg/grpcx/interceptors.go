package grpcx

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/restorna/platform/pkg/auth"
	"github.com/restorna/platform/pkg/tenancy"
	"github.com/rs/zerolog/log"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// AuthInterceptor verifies the bearer JWT on each unary request using pubKey and
// places the resulting tenancy.Scope into the context. pubKey may be raw 32-byte
// key material, base64-std, or base64-url encoded. Requests without a valid
// token are rejected with CodeUnauthenticated.
func AuthInterceptor(pubKey string) connect.Interceptor {
	pub := decodePubKey(pubKey)
	return &authInterceptor{pub: pub}
}

type authInterceptor struct {
	pub ed25519.PublicKey
}

func (i *authInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		if i.pub == nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("auth: no public key configured"))
		}
		header := req.Header().Get("Authorization")
		token := strings.TrimSpace(strings.TrimPrefix(header, "Bearer"))
		if token == "" {
			return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("auth: missing bearer token"))
		}
		claims, err := auth.Verify(i.pub, token)
		if err != nil {
			return nil, connect.NewError(connect.CodeUnauthenticated, err)
		}
		scope := tenancy.Scope{
			OwnerID:      claims.Owner,
			BrandID:      claims.Brand,
			RestaurantID: claims.Restaurant,
			Role:         claims.Role,
			UserID:       claims.UserID,
		}
		return next(tenancy.With(ctx, scope), req)
	}
}

func (i *authInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (i *authInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		if i.pub == nil {
			return connect.NewError(connect.CodeInternal, fmt.Errorf("auth: no public key configured"))
		}
		header := conn.RequestHeader().Get("Authorization")
		token := strings.TrimSpace(strings.TrimPrefix(header, "Bearer"))
		if token == "" {
			return connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("auth: missing bearer token"))
		}
		claims, err := auth.Verify(i.pub, token)
		if err != nil {
			return connect.NewError(connect.CodeUnauthenticated, err)
		}
		scope := tenancy.Scope{
			OwnerID:      claims.Owner,
			BrandID:      claims.Brand,
			RestaurantID: claims.Restaurant,
			Role:         claims.Role,
			UserID:       claims.UserID,
		}
		return next(tenancy.With(ctx, scope), conn)
	}
}

func decodePubKey(s string) ed25519.PublicKey {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	if b, err := base64.StdEncoding.DecodeString(s); err == nil && len(b) == ed25519.PublicKeySize {
		return ed25519.PublicKey(b)
	}
	if b, err := base64.RawStdEncoding.DecodeString(s); err == nil && len(b) == ed25519.PublicKeySize {
		return ed25519.PublicKey(b)
	}
	if b, err := base64.URLEncoding.DecodeString(s); err == nil && len(b) == ed25519.PublicKeySize {
		return ed25519.PublicKey(b)
	}
	if len(s) == ed25519.PublicKeySize {
		return ed25519.PublicKey([]byte(s))
	}
	return nil
}

// LoggingInterceptor logs each unary call with its procedure, duration, and
// resulting Connect code via zerolog.
func LoggingInterceptor() connect.Interceptor {
	return connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			start := time.Now()
			resp, err := next(ctx, req)
			ev := log.Info()
			code := connect.CodeOf(err)
			if err != nil {
				ev = log.Error().Err(err).Str("code", code.String())
			}
			ev.Str("procedure", req.Spec().Procedure).
				Dur("duration", time.Since(start)).
				Msg("grpcx: unary call")
			return resp, err
		}
	})
}

// OTelInterceptor wraps each unary call in an OpenTelemetry span named after the
// procedure, recording errors and status on the span.
func OTelInterceptor() connect.Interceptor {
	tracer := otel.Tracer("github.com/restorna/platform/pkg/grpcx")
	return connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			ctx, span := tracer.Start(ctx, req.Spec().Procedure,
				trace.WithSpanKind(trace.SpanKindServer),
				trace.WithAttributes(attribute.String("rpc.procedure", req.Spec().Procedure)),
			)
			defer span.End()
			resp, err := next(ctx, req)
			if err != nil {
				span.RecordError(err)
				span.SetStatus(codes.Error, connect.CodeOf(err).String())
			} else {
				span.SetStatus(codes.Ok, "")
			}
			return resp, err
		}
	})
}

// RecoverInterceptor converts a panic in a unary handler into a CodeInternal
// error instead of crashing the process.
func RecoverInterceptor() connect.Interceptor {
	return connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (resp connect.AnyResponse, err error) {
			defer func() {
				if r := recover(); r != nil {
					log.Error().Interface("panic", r).Str("procedure", req.Spec().Procedure).Msg("grpcx: recovered panic")
					err = connect.NewError(connect.CodeInternal, fmt.Errorf("internal error"))
				}
			}()
			return next(ctx, req)
		}
	})
}
