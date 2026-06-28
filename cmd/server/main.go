package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/breakfix/breakfix/internal/build"
	"github.com/breakfix/breakfix/internal/ca"
	"github.com/breakfix/breakfix/internal/challenge"
	"github.com/breakfix/breakfix/internal/config"
	"github.com/breakfix/breakfix/internal/db"
	"github.com/breakfix/breakfix/internal/k8s"
	"github.com/breakfix/breakfix/internal/proxy"
	"github.com/breakfix/breakfix/internal/server"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
	"k8s.io/klog/v2"

	pb "github.com/breakfix/breakfix/internal/proto"
)

// Methods that don't require a client certificate.
var allowAnon = map[string]bool{
	"/breakfix.Breakfix/Register":          true,
	"/breakfix.Breakfix/Login":             true,
	"/breakfix.Breakfix/GenerateChallenge": true,
}

func main() {
	configPath := flag.String("config", "breakfix.yaml", "Config file path")
	klog.InitFlags(nil)
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		klog.Fatalf("Failed to load config: %v", err)
	}
	_ = os.MkdirAll(cfg.DataDir, 0700)

	klog.InfoS("Breakfix API Server starting", "version", build.Version, "data_dir", cfg.DataDir)

	database, err := db.New(filepath.Join(cfg.DataDir, "breakfix.db"))
	if err != nil {
		klog.Fatalf("Failed to open database: %v", err)
	}
	defer func() { _ = database.Close() }()

	challengesDir := filepath.Join(cfg.DataDir, "challenges")
	if err := os.MkdirAll(challengesDir, 0755); err != nil {
		klog.Errorf("failed to create challenges dir: %v", err)
	}
	if err := challenge.SyncChallenges(database, challengesDir); err != nil {
		klog.Fatalf("Failed to sync challenges: %v", err)
	}

	caCert, err := ca.LoadOrCreate(cfg.CertFile(), cfg.KeyFile())
	if err != nil {
		klog.Fatalf("Failed to load CA: %v", err)
	}

	serverCert, serverKey, err := caCert.ServerCert()
	if err != nil {
		klog.Fatalf("Failed to generate server cert: %v", err)
	}
	tlsConfig, err := caCert.TLSConfig(serverCert, serverKey)
	if err != nil {
		klog.Fatalf("Failed to create TLS config: %v", err)
	}

	k8sClient, err := k8s.New(cfg.Kubeconfig)
	if err != nil {
		klog.Fatalf("Failed to create K8s client: %v", err)
	}

	cooldown := server.NewCooldownManager(database, k8sClient, nil)
	srv := server.New(database, k8sClient, cooldown, cfg, caCert)
	cooldown.SetCleanup(srv.CleanupInstance)

	grpcServer := grpc.NewServer(
		grpc.Creds(credentials.NewTLS(tlsConfig)),
		grpc.UnaryInterceptor(authInterceptor),
	)
	pb.RegisterBreakfixServer(grpcServer, srv)

	lis, err := net.Listen("tcp", fmt.Sprintf(":%d", cfg.Port))
	if err != nil {
		klog.Fatalf("port %d: %v", cfg.Port, err)
	}

	go proxy.Start(cfg.ProxyPort)

	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
		<-sig
		klog.InfoS("shutting down")
		cooldown.Stop()
		grpcServer.GracefulStop()
	}()

	klog.InfoS("listening", "port", cfg.Port, "proxy", cfg.ProxyPort)
	if err := grpcServer.Serve(lis); err != nil {
		klog.Errorf("grpc serve: %v", err)
	}
}

// authInterceptor checks client certificate for non-auth methods.
func authInterceptor(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
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
