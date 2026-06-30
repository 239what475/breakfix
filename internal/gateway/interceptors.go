package server

import (
	"context"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
	"log/slog"
)

func AuthInterceptor(allowAnon map[string]bool) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
		if allowAnon[info.FullMethod] {
			return handler(ctx, req)
		}
		p, ok := peer.FromContext(ctx)
		if !ok {
			return nil, status.Error(codes.Unauthenticated, "no peer")
		}
		tlsInfo, ok := p.AuthInfo.(credentials.TLSInfo)
		if !ok || len(tlsInfo.State.PeerCertificates) == 0 {
			return nil, status.Error(codes.Unauthenticated, "client certificate required")
		}
		return handler(ctx, req)
	}
}

func LoggingInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
		start := time.Now()
		resp, err := handler(ctx, req)
		d := time.Since(start)
		if err != nil {
			slog.Error("grpc completed", "method", info.FullMethod, "duration", d, "err", err)
		} else {
			slog.Info("grpc completed", "method", info.FullMethod, "duration", d)
		}
		return resp, err
	}
}
