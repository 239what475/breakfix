package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"os"
	"path/filepath"

	"github.com/breakfix/breakfix/internal/build"
	"github.com/breakfix/breakfix/internal/k8s"
	pb "github.com/breakfix/breakfix/internal/proto"
	"github.com/spf13/cobra"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"k8s.io/klog/v2"
)

var serverAddr, configDir string

func main() {
	home, _ := os.UserHomeDir()
	configDir = filepath.Join(home, ".breakfix")
	root := &cobra.Command{Use: "breakfix", Short: "Breakfix - SRE/DevOps interview practice platform"}
	root.PersistentFlags().StringVar(&serverAddr, "server", "localhost:9090", "API Server address")
	klog.InitFlags(nil)
	root.AddCommand(regCmd(), logCmd(), listC(), startC(), sshC(), subC(), stopC(), statC())
	root.Execute()
}

func grpcPlain() pb.BreakfixClient {
	conn, _ := grpc.NewClient(serverAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	return pb.NewBreakfixClient(conn)
}

func grpcMTLS() (pb.BreakfixClient, error) {
	certPEM, err := os.ReadFile(filepath.Join(configDir, "cert.pem"))
	if err != nil {
		return nil, fmt.Errorf("not logged in. Run 'breakfix login' first")
	}
	keyPEM, _ := os.ReadFile(filepath.Join(configDir, "key.pem"))
	cert, _ := tls.X509KeyPair(certPEM, keyPEM)
	conn, _ := grpc.NewClient(serverAddr, grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{
		Certificates: []tls.Certificate{cert}, InsecureSkipVerify: true,
	})))
	return pb.NewBreakfixClient(conn), nil
}

func regCmd() *cobra.Command {
	var u, p string
	c := &cobra.Command{Use: "register", Short: "Register", RunE: func(cmd *cobra.Command, args []string) error {
		r, err := grpcPlain().Register(context.Background(), &pb.RegisterRequest{Username: u, Password: p})
		if err != nil { return err }
		fmt.Println(r.TotpQr)
		os.MkdirAll(configDir, 0700)
		os.WriteFile(filepath.Join(configDir, "totp-secret"), []byte(r.TotpSecret), 0600)
		fmt.Println("Run: breakfix login")
		return nil
	}}
	c.Flags().StringVarP(&u, "user", "u", "", "Username")
	c.Flags().StringVarP(&p, "pass", "p", "", "Password")
	return c
}

func logCmd() *cobra.Command {
	var u, p, t string
	c := &cobra.Command{Use: "login", Short: "Login", RunE: func(cmd *cobra.Command, args []string) error {
		if t == "" { fmt.Print("Enter TOTP code: "); fmt.Scanln(&t) }
		r, err := grpcPlain().Login(context.Background(), &pb.LoginRequest{Username: u, Password: p, TotpCode: t})
		if err != nil { return err }
		os.MkdirAll(configDir, 0700)
		os.WriteFile(filepath.Join(configDir, "cert.pem"), []byte(r.ClientCert), 0600)
		os.WriteFile(filepath.Join(configDir, "key.pem"), []byte(r.ClientKey), 0600)
		fmt.Printf("✓ Logged in as %s (v%s)\n", r.Name, build.Version)
		return nil
	}}
	c.Flags().StringVarP(&u, "user", "u", "", "Username")
	c.Flags().StringVarP(&p, "pass", "p", "", "Password")
	c.Flags().StringVarP(&t, "totp", "t", "", "TOTP code")
	return c
}

func listC() *cobra.Command { return &cobra.Command{Use: "list", Short: "List", RunE: func(cmd *cobra.Command, args []string) error {
	c, err := grpcMTLS(); if err != nil { return err }
	r, _ := c.ListChallenges(context.Background(), &pb.ListChallengesRequest{})
	for _, ch := range r.Challenges { fmt.Printf("%-25s %-10s %s\n", ch.Id, ch.Type, ch.Title) }
	return nil
}}}

func startC() *cobra.Command { return &cobra.Command{Use: "start", Short: "Start", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
	c, err := grpcMTLS(); if err != nil { return err }
	r, _ := c.StartChallenge(context.Background(), &pb.StartChallengeRequest{ChallengeId: args[0]})
	fmt.Printf("Challenge: %s\nInstance:  %s\n\nRun: breakfix ssh %s\n", r.ChallengeTitle, r.InstanceId, r.InstanceId)
	return nil
}}}

func sshC() *cobra.Command { return &cobra.Command{Use: "ssh", Short: "SSH", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
	c, err := grpcMTLS(); if err != nil { return err }
	c.PingInstance(context.Background(), &pb.PingInstanceRequest{InstanceId: args[0]})
	k8sClient := k8s.EnsureK8sClient()
	ns, pod, err := k8sClient.ListPodsByInstance(args[0])
	if err != nil { return fmt.Errorf("pod not found: %w", err) }
	return k8s.ExecSSH(ns, pod)
}}}

func subC() *cobra.Command { return &cobra.Command{Use: "submit", Short: "Submit", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
	c, err := grpcMTLS(); if err != nil { return err }
	r, _ := c.SubmitChallenge(context.Background(), &pb.SubmitChallengeRequest{InstanceId: args[0]})
	if r.Passed { fmt.Println("✓ PASSED!") } else { fmt.Printf("✗ FAILED (exit=%d)\n", r.ExitCode) }
	return nil
}}}

func stopC() *cobra.Command { return &cobra.Command{Use: "stop", Short: "Stop", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
	c, err := grpcMTLS(); if err != nil { return err }
	c.StopChallenge(context.Background(), &pb.StopChallengeRequest{InstanceId: args[0]})
	fmt.Printf("Instance %s destroyed.\n", args[0])
	return nil
}}}

func statC() *cobra.Command { return &cobra.Command{Use: "status", Short: "Status", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
	c, err := grpcMTLS(); if err != nil { return err }
	r, _ := c.GetInstance(context.Background(), &pb.GetInstanceRequest{InstanceId: args[0]})
	fmt.Printf("Instance: %s Status: %d\n", r.InstanceId, r.Status)
	return nil
}}}
