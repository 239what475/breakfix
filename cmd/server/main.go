package main

import (
	"flag"
	"fmt"
	"log/slog"
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
	"google.golang.org/grpc/credentials"

	pb "github.com/breakfix/breakfix/internal/proto"
)

var allowAnon = map[string]bool{
	"/breakfix.Breakfix/Register":          true,
	"/breakfix.Breakfix/Login":             true,
	"/breakfix.Breakfix/GenerateChallenge": true,
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))

	configPath := flag.String("config", "breakfix.yaml", "Config file path")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		slog.Error("failed to load config", "err", err)
		os.Exit(1)
	}
	_ = os.MkdirAll(cfg.DataDir, 0700)

	slog.Info("Breakfix API Server starting", "version", build.Version, "data_dir", cfg.DataDir)

	database, err := db.New(filepath.Join(cfg.DataDir, "breakfix.db"))
	if err != nil {
		slog.Error("failed to open database", "err", err)
		os.Exit(1)
	}
	defer func() { _ = database.Close() }()

	challengesDir := filepath.Join(cfg.DataDir, "challenges")
	if err := os.MkdirAll(challengesDir, 0755); err != nil {
		slog.Error("failed to create challenges dir", "err", err)
	}
	if err := challenge.SyncChallenges(database, challengesDir); err != nil {
		slog.Error("failed to sync challenges", "err", err)
		os.Exit(1)
	}

	caCert, err := ca.LoadOrCreate(cfg.CertFile(), cfg.KeyFile())
	if err != nil {
		slog.Error("failed to load CA", "err", err)
		os.Exit(1)
	}

	host := cfg.ServerHost
	if host == "" {
		host = os.Getenv("SERVER_HOST")
	}
	serverCert, serverKey, err := caCert.ServerCert(host)
	if err != nil {
		slog.Error("failed to generate server cert", "err", err)
		os.Exit(1)
	}
	tlsConfig, err := caCert.TLSConfig(serverCert, serverKey)
	if err != nil {
		slog.Error("failed to create TLS config", "err", err)
		os.Exit(1)
	}

	k8sClient, err := k8s.New(cfg.Kubeconfig)
	if err != nil {
		slog.Error("failed to create K8s client", "err", err)
		os.Exit(1)
	}

	cooldown := server.NewCooldownManager(database, k8sClient, nil)
	srv := server.New(database, k8sClient, cooldown, cfg, caCert)
	cooldown.SetCleanup(srv.CleanupInstance)

	grpcServer := grpc.NewServer(
		grpc.Creds(credentials.NewTLS(tlsConfig)),
		grpc.UnaryInterceptor(server.AuthInterceptor(allowAnon)),
	)
	pb.RegisterBreakfixServer(grpcServer, srv)

	lis, err := net.Listen("tcp", fmt.Sprintf(":%d", cfg.Port))
	if err != nil {
		slog.Error("failed to listen", "port", cfg.Port, "err", err)
		os.Exit(1)
	}

	go proxy.Start(cfg.ProxyPort)

	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
		<-sig
		slog.Info("shutting down")
		cooldown.Stop()
		grpcServer.GracefulStop()
	}()

	slog.Info("listening", "port", cfg.Port, "proxy", cfg.ProxyPort)
	if err := grpcServer.Serve(lis); err != nil {
		slog.Error("grpc serve error", "err", err)
	}
}

