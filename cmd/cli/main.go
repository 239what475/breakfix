package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"

	"github.com/breakfix/breakfix/internal/build"
	"github.com/breakfix/breakfix/internal/k8s"
	pb "github.com/breakfix/breakfix/internal/proto"
	"github.com/spf13/cobra"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"k8s.io/klog/v2"
)

var (
	serverAddr string
	client     pb.BreakfixClient
	certFile   string
	keyFile    string
	caFile     string
)

func main() {
	rootCmd := &cobra.Command{
		Use:   "breakfix",
		Short: "Breakfix - SRE/DevOps interview practice platform",
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			tlsConfig, err := loadClientTLS(certFile, keyFile, caFile)
			if err != nil {
				return fmt.Errorf("TLS config: %w", err)
			}
			conn, err := grpc.NewClient(serverAddr,
				grpc.WithTransportCredentials(credentials.NewTLS(tlsConfig)),
			)
			if err != nil {
				return fmt.Errorf("connect to server: %w", err)
			}
			client = pb.NewBreakfixClient(conn)
			return nil
		},
	}

	klog.InitFlags(nil)
	rootCmd.PersistentFlags().StringVar(&serverAddr, "server", "localhost:9090", "API Server address")
	rootCmd.PersistentFlags().StringVar(&certFile, "cert", "", "Client certificate file (PEM)")
	rootCmd.PersistentFlags().StringVar(&keyFile, "key", "", "Client key file (PEM)")
	rootCmd.PersistentFlags().StringVar(&caFile, "ca", "", "CA certificate file (PEM)")

	rootCmd.AddCommand(
		loginCmd(), listCmd(), startCmd(), sshCmd(),
		submitCmd(), stopCmd(), statusCmd(),
	)

	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func loadClientTLS(certFile, keyFile, caFile string) (*tls.Config, error) {
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("load client cert: %w", err)
	}

	caPEM, err := os.ReadFile(caFile)
	if err != nil {
		return nil, fmt.Errorf("read CA file: %w", err)
	}
	caPool := x509.NewCertPool()
	if !caPool.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("failed to parse CA certificate")
	}

	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		RootCAs:      caPool,
		MinVersion:   tls.VersionTLS12,
	}, nil
}

func loginCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "login",
		Short: "Login to Breakfix",
		RunE: func(cmd *cobra.Command, args []string) error {
			resp, err := client.WhoAmI(context.Background(), &pb.WhoAmIRequest{})
			if err != nil {
				return fmt.Errorf("login failed: %w", err)
			}
			fmt.Printf("✓ Logged in as %s (v%s)\n", resp.Name, build.Version)
			if resp.IsNew {
				fmt.Println("  Welcome to Breakfix!")
			}
			return nil
		},
	}
}

func listCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List available challenges",
		RunE: func(cmd *cobra.Command, args []string) error {
			resp, err := client.ListChallenges(context.Background(), &pb.ListChallengesRequest{})
			if err != nil {
				return err
			}
			if len(resp.Challenges) == 0 {
				fmt.Println("No challenges available.")
				return nil
			}
			fmt.Printf("%-25s %-10s %-8s %s\n", "ID", "TYPE", "DIFF", "TITLE")
			fmt.Println("-----------------------------------------------------------")
			for _, c := range resp.Challenges {
				solved := ""
				if c.Solved {
					solved = " ✓"
				}
				fmt.Printf("%-25s %-10s %-8s %s%s\n", c.Id, c.Type, c.Difficulty, c.Title, solved)
			}
			return nil
		},
	}
}

func startCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "start <challenge-id>",
		Short: "Start a challenge instance",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			resp, err := client.StartChallenge(context.Background(), &pb.StartChallengeRequest{ChallengeId: args[0]})
			if err != nil {
				return err
			}
			fmt.Printf("Challenge: %s\nInstance:  %s\nTimeout:   %ds\n\nRun: breakfix ssh %s\n",
				resp.ChallengeTitle, resp.InstanceId, resp.TimeoutSec, resp.InstanceId)
			return nil
		},
	}
}

func sshCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "ssh <instance-id>",
		Short: "SSH into a challenge instance",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			instanceID := args[0]

			if _, err := client.PingInstance(context.Background(), &pb.PingInstanceRequest{InstanceId: instanceID}); err != nil {
				klog.V(1).InfoS("ping failed, continuing anyway", "instance", instanceID, "err", err)
			}

			k8sClient := k8s.EnsureK8sClient()
			ns, pod, err := k8sClient.ListPodsByInstance(instanceID)
			if err != nil {
				return fmt.Errorf("pod not found: %w", err)
			}
			return k8s.ExecSSH(ns, pod)
		},
	}
}

func submitCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "submit <instance-id>",
		Short: "Submit for verification",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			resp, err := client.SubmitChallenge(context.Background(), &pb.SubmitChallengeRequest{InstanceId: args[0]})
			if err != nil {
				return err
			}
			if resp.Passed {
				fmt.Println("✓ PASSED!")
			} else {
				fmt.Printf("✗ FAILED (exit=%d)\n", resp.ExitCode)
			}
			if resp.Output != "" {
				fmt.Println("--- output ---")
				fmt.Println(resp.Output)
			}
			return nil
		},
	}
}

func stopCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stop <instance-id>",
		Short: "Stop and destroy instance",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, err := client.StopChallenge(context.Background(), &pb.StopChallengeRequest{InstanceId: args[0]}); err != nil {
				return err
			}
			fmt.Printf("Instance %s destroyed.\n", args[0])
			return nil
		},
	}
}

func statusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status <instance-id>",
		Short: "Check instance status",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, err := client.PingInstance(context.Background(), &pb.PingInstanceRequest{InstanceId: args[0]}); err != nil {
				klog.V(1).InfoS("ping failed, showing cached status", "instance", args[0], "err", err)
			}
			resp, err := client.GetInstance(context.Background(), &pb.GetInstanceRequest{InstanceId: args[0]})
			if err != nil {
				return err
			}
			s := "running"
			switch resp.Status {
			case pb.InstanceStatus_DRAINING:
				s = "draining"
			case pb.InstanceStatus_DESTROYED:
				s = "destroyed"
			}
			fmt.Printf("Instance:  %s\nChallenge: %s\nStatus:    %s\n", resp.InstanceId, resp.ChallengeId, s)
			return nil
		},
	}
}
