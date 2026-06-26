package main

import (
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/breakfix/breakfix/internal/build"
	"github.com/breakfix/breakfix/internal/challenge"
	"github.com/breakfix/breakfix/internal/db"
	"github.com/breakfix/breakfix/internal/k8s"
	bl "github.com/breakfix/breakfix/internal/log"
	"github.com/breakfix/breakfix/internal/server"
	"google.golang.org/grpc"
	"k8s.io/klog/v2"

	pb "github.com/breakfix/breakfix/internal/proto"
)

func main() {
	kubeconfigPath := flag.String("kubeconfig", "", "Path to kubeconfig file (empty = default)")
	dbPath := flag.String("db", "breakfix.db", "SQLite database path")
	challengesDir := flag.String("challenges", "./challenges", "Challenges directory")
	port := flag.Int("port", 9090, "gRPC port")

	bl.Init()
	flag.Parse()

	klog.InfoS("Breakfix API Server starting",
		"version", build.Version,
		"mode", build.Mode,
	)

	kubeconfig := *kubeconfigPath
	if kubeconfig == "" && build.IsProd() {
		kubeconfig = "/etc/breakfix/kubeconfig"
	}
	k8sClient, err := k8s.New(kubeconfig)
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
	klog.V(2).InfoS("challenges synced", "dir", *challengesDir)

	// Cooldown manager
	cooldown := server.NewCooldownManager(database, k8sClient)

	// gRPC server
	srv := server.New(database, k8sClient, cooldown, *challengesDir)
	grpcServer := grpc.NewServer()
	pb.RegisterBreakfixServer(grpcServer, srv)

	lis, err := net.Listen("tcp", fmt.Sprintf(":%d", *port))
	if err != nil {
		klog.Fatalf("Failed to listen: %v", err)
	}

	// Graceful shutdown
	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
		<-sig
		klog.InfoS("shutting down")
		cooldown.Stop()
		grpcServer.GracefulStop()
	}()

	// Periodic cooldown check
	go func() {
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for range t.C {
			cooldown.CheckDraining()
		}
	}()

	klog.InfoS("listening", "port", *port)
	if err := grpcServer.Serve(lis); err != nil {
		klog.Fatalf("Failed to serve: %v", err)
	}
}
