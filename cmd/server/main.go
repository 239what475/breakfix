package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"net/http"
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
	"github.com/breakfix/breakfix/internal/server"
	"github.com/elazarl/goproxy"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
	"k8s.io/klog/v2"

	pb "github.com/breakfix/breakfix/internal/proto"
)

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
	os.MkdirAll(challengesDir, 0755)
	if err := challenge.SyncChallenges(database, challengesDir); err != nil {
		klog.Fatalf("Failed to sync challenges: %v", err)
	}

	ca, err := ca.LoadOrCreate(cfg.CertFile(), cfg.KeyFile())
	if err != nil {
		klog.Fatalf("Failed to load CA: %v", err)
	}

	serverCert, serverKey, err := ca.ServerCert()
	if err != nil {
		klog.Fatalf("Failed to generate server cert: %v", err)
	}
	tlsConfig, err := ca.TLSConfig(serverCert, serverKey)
	if err != nil {
		klog.Fatalf("Failed to create TLS config: %v", err)
	}

	k8sClient, err := k8s.New(cfg.Kubeconfig)
	if err != nil {
		klog.Fatalf("Failed to create K8s client: %v", err)
	}

	cooldown := server.NewCooldownManager(database, k8sClient)
	srv := server.New(database, k8sClient, cooldown, cfg, ca)

	publicServer := grpc.NewServer(grpc.UnaryInterceptor(authPublicOnly))
	pb.RegisterBreakfixServer(publicServer, srv)

	secureServer := grpc.NewServer(
		grpc.Creds(credentials.NewTLS(tlsConfig)),
		grpc.UnaryInterceptor(authRequireCert),
	)
	pb.RegisterBreakfixServer(secureServer, srv)

	go func() {
		klog.InfoS("proxy listening", "port", cfg.ProxyPort)
		http.ListenAndServe(fmt.Sprintf(":%d", cfg.ProxyPort), goproxy.NewProxyHttpServer())
	}()

	publicLis, _ := net.Listen("tcp", fmt.Sprintf(":%d", cfg.Port))
	secureLis, _ := net.Listen("tcp", fmt.Sprintf(":%d", cfg.MTLSPort))

	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
		<-sig
		klog.InfoS("shutting down")
		cooldown.Stop(); publicServer.GracefulStop(); secureServer.GracefulStop()
	}()

	klog.InfoS("listening", "public", cfg.Port, "mtls", cfg.MTLSPort, "proxy", cfg.ProxyPort)

	go func() { publicServer.Serve(publicLis) }()
	secureServer.Serve(secureLis)
}

var publicMethods = map[string]bool{
	"/breakfix.Breakfix/Register": true,
	"/breakfix.Breakfix/Login":    true,
}

func authPublicOnly(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
	if !publicMethods[info.FullMethod] {
		return nil, status.Error(codes.PermissionDenied, "only register/login allowed on this port")
	}
	return handler(ctx, req)
}

func authRequireCert(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
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
