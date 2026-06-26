package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"

	"github.com/breakfix/breakfix/internal/build"
	pb "github.com/breakfix/breakfix/internal/proto"
	"github.com/spf13/cobra"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

var (
	serverAddr string
	client     pb.BreakfixClient
)

func main() {
	rootCmd := &cobra.Command{
		Use:   "breakfix",
		Short: "Breakfix - SRE/DevOps interview practice platform",
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			conn, err := grpc.NewClient(serverAddr,
				grpc.WithTransportCredentials(insecure.NewCredentials()),
			)
			if err != nil {
				return fmt.Errorf("connect to server: %w", err)
			}
			client = pb.NewBreakfixClient(conn)
			return nil
		},
	}

	rootCmd.PersistentFlags().StringVar(&serverAddr, "server", "localhost:9090", "API Server address")
	rootCmd.AddCommand(
		loginCmd(), listCmd(), startCmd(), sshCmd(),
		submitCmd(), stopCmd(), statusCmd(),
	)

	if build.IsDev() {
		fmt.Fprintf(os.Stderr, "[DEV MODE] Breakfix CLI %s\n", build.Version)
	}
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func loginCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "login",
		Short: "Login to Breakfix",
		RunE: func(cmd *cobra.Command, args []string) error {
			if !build.IsDev() {
				return fmt.Errorf("run 'tsh login' first, then breakfix login")
			}
			resp, err := client.WhoAmI(context.Background(), &pb.WhoAmIRequest{Subject: "dev-user"})
			if err != nil {
				return fmt.Errorf("login failed: %w", err)
			}
			fmt.Printf("✓ Logged in as %s (dev mode)\n", resp.Name)
			if resp.IsNew {
				fmt.Println("  New account created!")
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

			// Revive if draining
			client.PingInstance(context.Background(), &pb.PingInstanceRequest{InstanceId: instanceID}) //nolint:errcheck

			if build.IsDev() {
				// Find pod by instance ID label
				nsBytes, _ := exec.Command("kubectl", "get", "pod", "-A",
					"-l", fmt.Sprintf("instance-id=%s", instanceID),
					"-o", "jsonpath={.items[0].metadata.namespace}").Output()
				podBytes, _ := exec.Command("kubectl", "get", "pod", "-A",
					"-l", fmt.Sprintf("instance-id=%s", instanceID),
					"-o", "jsonpath={.items[0].metadata.name}").Output()
				ns, pod := string(nsBytes), string(podBytes)
				if pod == "" {
					return fmt.Errorf("pod not found for instance %s", instanceID)
				}

				sshCmd := exec.Command("kubectl", "exec", "-ti", "-n", ns, pod, "--", "/bin/bash")
				sshCmd.Stdin = os.Stdin
				sshCmd.Stdout = os.Stdout
				sshCmd.Stderr = os.Stderr
				return sshCmd.Run()
			}
			return fmt.Errorf("prod SSH: use tsh kubectl exec")
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
			client.PingInstance(context.Background(), &pb.PingInstanceRequest{InstanceId: args[0]}) //nolint:errcheck
			resp, err := client.GetInstance(context.Background(), &pb.GetInstanceRequest{InstanceId: args[0]})
			if err != nil {
				return err
			}
			s := "running"
			if resp.Status == pb.InstanceStatus_DRAINING {
				s = "draining"
			} else if resp.Status == pb.InstanceStatus_DESTROYED {
				s = "destroyed"
			}
			fmt.Printf("Instance:  %s\nChallenge: %s\nStatus:    %s\n", resp.InstanceId, resp.ChallengeId, s)
			return nil
		},
	}
}
