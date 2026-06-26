package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/breakfix/breakfix/internal/build"
	"github.com/breakfix/breakfix/internal/challenge"
	"github.com/breakfix/breakfix/internal/db"
	"github.com/breakfix/breakfix/internal/k8s"
	"github.com/breakfix/breakfix/internal/server"
	"google.golang.org/grpc"

	pb "github.com/breakfix/breakfix/internal/proto"
)

func main() {
	configPath := flag.String("config", "server.yaml", "Config file path")
	dbPath := flag.String("db", "breakfix.db", "SQLite database path")
	challengesDir := flag.String("challenges", "./challenges", "Challenges directory")
	port := flag.Int("port", 9090, "gRPC port")
	flag.Parse()

	log.SetFlags(log.LstdFlags | log.Lshortfile)
	if build.IsDev() {
		log.SetFlags(log.LstdFlags | log.Lshortfile)
		log.Println("===== BREAKFIX DEV MODE =====")
	}

	var kubeconfig string
	if !build.IsDev() {
		// In prod, read from config
		kubeconfig = filepath.Join(filepath.Dir(*configPath), "kubeconfig")
	}
	k8sClient := k8s.New(kubeconfig)

	// Database
	database, err := db.New(*dbPath)
	if err != nil {
		log.Fatalf("Failed to open database: %v", err)
	}
	defer func() { _ = database.Close() }()

	// Sync challenges
	if err := challenge.SyncChallenges(database, *challengesDir); err != nil {
		log.Fatalf("Failed to sync challenges: %v", err)
	}
	log.Printf("Challenges synced from %s", *challengesDir)

	// Cooldown manager
	cooldown := server.NewCooldownManager(database, k8sClient)

	// gRPC server
	srv := server.New(database, k8sClient, cooldown, *challengesDir)
	grpcServer := grpc.NewServer()
	pb.RegisterBreakfixServer(grpcServer, srv)

	lis, err := net.Listen("tcp", fmt.Sprintf(":%d", *port))
	if err != nil {
		log.Fatalf("Failed to listen: %v", err)
	}

	// Graceful shutdown
	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
		<-sig
		log.Println("Shutting down...")
		cooldown.Stop()
		grpcServer.GracefulStop()
	}()

	go func() {
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for range t.C {
			cooldown.CheckDraining()
		}
	}()

	log.Printf("Breakfix API Server %s starting on :%d (mode=%s)", build.Version, *port, build.Mode)
	if err := grpcServer.Serve(lis); err != nil {
		log.Fatalf("Failed to serve: %v", err)
	}
}
