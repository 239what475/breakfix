package main

import (
	"context"
	"net/http"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/breakfix/breakfix/internal/build"
	"github.com/breakfix/breakfix/internal/ca"
	"github.com/breakfix/breakfix/internal/challenge"
	"github.com/breakfix/breakfix/internal/db"
	"github.com/breakfix/breakfix/internal/k8s"
	"github.com/breakfix/breakfix/internal/server"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
	"k8s.io/klog/v2"
	"github.com/elazarl/goproxy"

	pb "github.com/breakfix/breakfix/internal/proto"
)

func main() {
	kubeconfigPath := flag.String("kubeconfig", "", "Path to kubeconfig")
	dbPath := flag.String("db", "breakfix.db", "SQLite database path")
	challengesDir := flag.String("challenges", "./challenges", "Challenges directory")
	port := flag.Int("port", 9090, "gRPC port")

	klog.InitFlags(nil)
	flag.Parse()

	klog.InfoS("Breakfix API Server starting", "version", build.Version)

	// K8s client
	k8sClient, err := k8s.New(*kubeconfigPath)
	if err != nil {
		klog.Fatalf("Failed to create K8s client: %v", err)
	}

	// Database
	database, err := db.New(*dbPath)
	if err != nil {
		klog.Fatalf("Failed to open database: %v", err)
	}
	defer func() { _ = database.Close() }()

	// Sync challenges
	if err := challenge.SyncChallenges(database, *challengesDir); err != nil {
		klog.Fatalf("Failed to sync challenges: %v", err)
	}
	klog.InfoS("challenges synced", "dir", *challengesDir)

	// CA (auto-generates on first run)
	ca, err := ca.New()
	if err != nil {
		klog.Fatalf("Failed to create CA: %v", err)
	}

	// Server cert
	serverCert, serverKey, err := ca.ServerCert()
	if err != nil {
		klog.Fatalf("Failed to generate server cert: %v", err)
	}

	// mTLS config
	tlsConfig, err := ca.TLSConfig(serverCert, serverKey)
	if err != nil {
		klog.Fatalf("Failed to create TLS config: %v", err)
	}

	// Cooldown manager
	cooldown := server.NewCooldownManager(database, k8sClient)

	srv := server.New(database, k8sClient, cooldown, *challengesDir, ca)

	// Public port: plain gRPC, only register/login
	publicServer := grpc.NewServer(grpc.UnaryInterceptor(authPublicOnly))
	pb.RegisterBreakfixServer(publicServer, srv)

	// mTLS port: requires client certificate
	secureServer := grpc.NewServer(
		grpc.Creds(credentials.NewTLS(tlsConfig)),
		grpc.UnaryInterceptor(authRequireCert),
	)
	pb.RegisterBreakfixServer(secureServer, srv)

	// Start public listener
	publicLis, err := net.Listen("tcp", fmt.Sprintf(":%d", *port))
	if err != nil {
		klog.Fatalf("Failed to listen public: %v", err)
	}

	// Start mTLS listener
	mtlsPort := *port + 443
	secureLis, err := net.Listen("tcp", fmt.Sprintf(":%d", mtlsPort))
	if err != nil {
		klog.Fatalf("Failed to listen mTLS: %v", err)
	}

	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
		<-sig
		klog.InfoS("shutting down")
		cooldown.Stop()
		publicServer.GracefulStop()
		secureServer.GracefulStop()
	}()

	go func() {
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for range t.C {
			cooldown.CheckDraining()
		}
	}()

	startProxy()
	klog.InfoS("listening", "public", *port, "mtls", mtlsPort)

	go func() {
		if err := publicServer.Serve(publicLis); err != nil {
			klog.Fatalf("Public server failed: %v", err)
		}
	}()
	if err := secureServer.Serve(secureLis); err != nil {
		klog.Fatalf("mTLS server failed: %v", err)
	}
}

var publicMethods = map[string]bool{
	"/breakfix.Breakfix/Register": true,
	"/breakfix.Breakfix/Login":    true,
}

// authPublicOnly allows only register/login on the public port.
func authPublicOnly(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
	if !publicMethods[info.FullMethod] {
		return nil, status.Error(codes.PermissionDenied, "only register/login allowed on this port, use mTLS port")
	}
	return handler(ctx, req)
}

// authRequireCert requires a valid client certificate for all methods.
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

func startProxy() {
	go func() {
		klog.InfoS("proxy listening", "port", 3128)
		if err := http.ListenAndServe(":3128", goproxy.NewProxyHttpServer()); err != nil {
			klog.Fatalf("Proxy failed: %v", err)
		}
	}()
}
