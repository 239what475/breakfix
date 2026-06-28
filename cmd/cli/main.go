package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"golang.org/x/term"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"path/filepath"

	"github.com/breakfix/breakfix/internal/build"
	pb "github.com/breakfix/breakfix/internal/proto"
	"github.com/spf13/cobra"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"k8s.io/klog/v2"
)

var serverAddr, configDir string

func main() {
	home, _ := os.UserHomeDir()
	configDir = filepath.Join(home, ".breakfix")
	root := &cobra.Command{Use: "breakfix", Short: "Breakfix - SRE/DevOps interview practice platform"}
	root.PersistentFlags().StringVar(&serverAddr, "server", "localhost", "API Server hostname (:9090 auto-derived)")
	klog.InitFlags(nil)
	root.AddCommand(regCmd(), logCmd(), listC(), startC(), sshC(), subC(), stopC(), statC(), genCmd())
	root.Execute()
}

// grpcDial returns a gRPC client connection using TLS.
// On first use (no saved CA cert), InsecureSkipVerify is set.
// After register, the CA cert is saved and used for verification.
// After login, client certificates are also presented (mTLS).
func grpcDial() (pb.BreakfixClient, *grpc.ClientConn, error) {
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12}

	// Try to load CA cert for server verification
	caPEM, _ := os.ReadFile(filepath.Join(configDir, "ca-cert.pem"))
	if len(caPEM) > 0 {
		cp := x509.NewCertPool()
		cp.AppendCertsFromPEM(caPEM)
		tlsCfg.RootCAs = cp
	} else {
		tlsCfg.InsecureSkipVerify = true // first-time: skip, but save CA after register
	}

	// Try to load client cert for mTLS
	certPEM, _ := os.ReadFile(filepath.Join(configDir, "cert.pem"))
	keyPEM, _ := os.ReadFile(filepath.Join(configDir, "key.pem"))
	if len(certPEM) > 0 && len(keyPEM) > 0 {
		cert, _ := tls.X509KeyPair(certPEM, keyPEM)
		tlsCfg.Certificates = []tls.Certificate{cert}
	}

	conn, err := grpc.NewClient(serverAddr+":9090", grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg)))
	if err != nil {
		return nil, nil, err
	}
	return pb.NewBreakfixClient(conn), conn, nil
}

func saveCA(caCert string) {
	os.MkdirAll(configDir, 0700)
	os.WriteFile(filepath.Join(configDir, "ca-cert.pem"), []byte(caCert), 0644)
}

func regCmd() *cobra.Command {
	var u, p string
	c := &cobra.Command{Use: "register", Short: "Register", RunE: func(cmd *cobra.Command, args []string) error {
		c, conn, err := grpcDial()
		if err != nil { return err }
		defer conn.Close()

		r, err := c.Register(context.Background(), &pb.RegisterRequest{Username: u, Password: p})
		if err != nil { return err }
		fmt.Println(r.TotpQr)

		os.MkdirAll(configDir, 0700)
		os.WriteFile(filepath.Join(configDir, "totp-secret"), []byte(r.TotpSecret), 0600)
		saveCA(r.CaCert)
		fmt.Printf("\nRun: breakfix login -u %s -p <password>\n", u)
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

		c, conn, err := grpcDial()
		if err != nil { return err }
		defer conn.Close()

		r, err := c.Login(context.Background(), &pb.LoginRequest{Username: u, Password: p, TotpCode: t})
		if err != nil { return err }
		os.MkdirAll(configDir, 0700)
		os.WriteFile(filepath.Join(configDir, "cert.pem"), []byte(r.ClientCert), 0600)
		os.WriteFile(filepath.Join(configDir, "key.pem"), []byte(r.ClientKey), 0600)
		saveCA(r.CaCert)
		fmt.Printf("✓ Logged in as %s (v%s)\n", r.Name, build.Version)
		return nil
	}}
	c.Flags().StringVarP(&u, "user", "u", "", "Username")
	c.Flags().StringVarP(&p, "pass", "p", "", "Password")
	c.Flags().StringVarP(&t, "totp", "t", "", "TOTP code")
	return c
}

func listC() *cobra.Command { return &cobra.Command{Use: "list", Short: "List", RunE: func(cmd *cobra.Command, args []string) error {
	c, conn, err := grpcDial(); if err != nil { return err }; defer conn.Close()
	r, err := c.ListChallenges(context.Background(), &pb.ListChallengesRequest{})
		if err != nil { return err }
	for _, ch := range r.Challenges { fmt.Printf("%-25s %-10s %s\n", ch.Id, ch.Type, ch.Title) }
	return nil
}}}

func startC() *cobra.Command { return &cobra.Command{Use: "start", Short: "Start", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
	c, conn, err := grpcDial(); if err != nil { return err }; defer conn.Close()
	r, err := c.StartChallenge(context.Background(), &pb.StartChallengeRequest{ChallengeId: args[0]})
		if err != nil { return err }
	fmt.Printf("Challenge: %s\nInstance:  %s\n\nRun: breakfix ssh %s\n", r.ChallengeTitle, r.InstanceId, r.InstanceId)
	return nil
}}}

func sshC() *cobra.Command { return &cobra.Command{Use: "ssh", Short: "SSH", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		c, conn, err := grpcDial(); if err != nil { return err }; defer conn.Close()
		c.PingInstance(context.Background(), &pb.PingInstanceRequest{InstanceId: args[0]})
		stream, err := c.ExecInstance(context.Background())
		if err != nil { return fmt.Errorf("exec: %w", err) }
		stream.Send(&pb.PTYData{Data: []byte(args[0])})

		// Terminal setup (only if stdin is a terminal)
		if term.IsTerminal(int(os.Stdin.Fd())) {
			oldState, err := term.MakeRaw(int(os.Stdin.Fd()))
			if err != nil { return err }
			defer term.Restore(int(os.Stdin.Fd()), oldState)

			sigCh := make(chan os.Signal, 1)
			signal.Notify(sigCh, syscall.SIGWINCH)
			defer signal.Stop(sigCh)
			go func() {
				if w, h, err := term.GetSize(int(os.Stdin.Fd())); err == nil {
					stream.Send(&pb.PTYData{Cols: uint32(w), Rows: uint32(h)})
				}
				for range sigCh {
					if w, h, err := term.GetSize(int(os.Stdin.Fd())); err == nil {
						stream.Send(&pb.PTYData{Cols: uint32(w), Rows: uint32(h)})
					}
				}
			}()
		}

		// stdin → stream
		go func() {
			buf := make([]byte, 4096)
			for {
				n, err := os.Stdin.Read(buf)
				if n > 0 { stream.Send(&pb.PTYData{Data: buf[:n]}) }
				if err != nil { stream.CloseSend(); return }
			}
		}()

		// stream → stdout
		for {
			data, err := stream.Recv()
			if err != nil { if err != io.EOF { fmt.Fprintf(os.Stderr, "\nssh: %v\n", err) }; return nil }
			os.Stdout.Write(data.Data)
		}
	}}}


func subC() *cobra.Command { return &cobra.Command{Use: "submit", Short: "Submit", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
	c, conn, err := grpcDial(); if err != nil { return err }; defer conn.Close()
	r, err := c.SubmitChallenge(context.Background(), &pb.SubmitChallengeRequest{InstanceId: args[0]})
		if err != nil { return err }
	if r.Passed { fmt.Println("✓ PASSED!") } else { fmt.Printf("✗ FAILED (exit=%d)\n", r.ExitCode) }
	return nil
}}}

func stopC() *cobra.Command { return &cobra.Command{Use: "stop", Short: "Stop", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
	c, conn, err := grpcDial(); if err != nil { return err }; defer conn.Close()
	c.StopChallenge(context.Background(), &pb.StopChallengeRequest{InstanceId: args[0]})
	fmt.Printf("Instance %s destroyed.\n", args[0])
	return nil
}}}

func statC() *cobra.Command { return &cobra.Command{Use: "status", Short: "Status", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
	c, conn, err := grpcDial(); if err != nil { return err }; defer conn.Close()
	r, err := c.GetInstance(context.Background(), &pb.GetInstanceRequest{InstanceId: args[0]})
		if err != nil { return err }
	fmt.Printf("Instance: %s Status: %d\n", r.InstanceId, r.Status)
	return nil
}}}

func genCmd() *cobra.Command {
	var topic string
	c := &cobra.Command{Use: "generate", Short: "Generate a challenge via agent", RunE: func(cmd *cobra.Command, args []string) error {
		c, conn, err := grpcDial(); if err != nil { return err }; defer conn.Close()
		fmt.Printf("Generating challenge for: %s\n", topic)
		r, err := c.GenerateChallenge(context.Background(), &pb.GenerateChallengeRequest{Topic: topic})
		if err != nil { return err }
		fmt.Printf("Status: %s\n", r.Status)
		if r.Detail != "" { fmt.Println(r.Detail) }
		return nil
	}}
	c.Flags().StringVar(&topic, "topic", "", "Challenge topic description")
	return c
}
