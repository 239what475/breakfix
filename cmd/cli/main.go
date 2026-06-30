package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/term"

	"github.com/breakfix/breakfix/internal/build"
	pb "github.com/breakfix/breakfix/pkg/proto"
	"github.com/spf13/cobra"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"log/slog"
	"rsc.io/qr"
)

var serverAddr, configDir string

func main() {
	home, _ := os.UserHomeDir()
	configDir = filepath.Join(home, ".breakfix")
	root := &cobra.Command{Use: "breakfix", Short: "Breakfix - SRE/DevOps interview practice platform"}
	root.PersistentFlags().StringVar(&serverAddr, "server", "localhost", "API Server hostname (:9090 auto-derived)")
	root.AddCommand(regCmd(), logCmd(), listC(), startC(), subC(), resetC(), genCmd())
	root.Execute() //nolint:errcheck
}

func grpcDial() (pb.BreakfixClient, *grpc.ClientConn, error) {
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12}

	caPEM, _ := os.ReadFile(filepath.Join(configDir, "ca-cert.pem"))
	if len(caPEM) > 0 {
		cp := x509.NewCertPool()
		cp.AppendCertsFromPEM(caPEM)
		tlsCfg.RootCAs = cp
	} else {
		tlsCfg.InsecureSkipVerify = true
	}

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
	if err := os.MkdirAll(configDir, 0700); err != nil {
		slog.Error("mkdir config dir", "err", err)
		return
	}
	if err := os.WriteFile(filepath.Join(configDir, "ca-cert.pem"), []byte(caCert), 0644); err != nil {
		slog.Error("write ca cert", "err", err)
	}
}

func closeConn(conn *grpc.ClientConn) {
	if err := conn.Close(); err != nil {
		slog.Debug("close conn", "err", err)
	}
}

func regCmd() *cobra.Command {
	var u, p string
	c := &cobra.Command{Use: "register", Short: "Register", RunE: func(cmd *cobra.Command, args []string) error {
		c, conn, err := grpcDial()
		if err != nil {
			return err
		}
		defer closeConn(conn)

		r, err := c.Register(context.Background(), &pb.RegisterRequest{Username: u, Password: p})
		if err != nil {
			return err
		}

		// Render QR code locally
		renderQR(r.TotpUrl)

		if err := os.MkdirAll(configDir, 0700); err != nil {
			return fmt.Errorf("mkdir config: %w", err)
		}
		if err := os.WriteFile(filepath.Join(configDir, "totp-secret"), []byte(r.TotpSecret), 0600); err != nil {
			return fmt.Errorf("write totp: %w", err)
		}
		saveCA(r.CaCert)
		fmt.Printf("\nRun: breakfix login -u %s -p <password>\n", u)
		return nil
	}}
	c.Flags().StringVarP(&u, "user", "u", "", "Username")
	c.Flags().StringVarP(&p, "pass", "p", "", "Password")
	return c
}

func renderQR(url string) {
	code, err := qr.Encode(url, qr.L)
	if err != nil {
		return
	}

	const (
		bgBlack = "\033[40m  \033[0m"
		bgWhite = "\033[47m  \033[0m"
		qz      = "\033[47m  \033[0m"
	)

	scale := code.Size
	var sb strings.Builder
	sb.WriteByte('\n')

	qzCol := strings.Repeat(qz, 2)
	qzRow := strings.Repeat(qz, scale+4)

	for i := 0; i < 2; i++ {
		sb.WriteString(qzRow)
		sb.WriteByte('\n')
	}
	for y := 0; y < scale; y++ {
		sb.WriteString(qzCol)
		for x := 0; x < scale; x++ {
			if code.Black(x, y) {
				sb.WriteString(bgBlack)
			} else {
				sb.WriteString(bgWhite)
			}
		}
		sb.WriteString(qzCol)
		sb.WriteByte('\n')
	}
	for i := 0; i < 2; i++ {
		sb.WriteString(qzRow)
		sb.WriteByte('\n')
	}

	fmt.Print(sb.String())
}

func logCmd() *cobra.Command {
	var u, p, t string
	c := &cobra.Command{Use: "login", Short: "Login", RunE: func(cmd *cobra.Command, args []string) error {
		if t == "" {
			fmt.Print("Enter TOTP code: ")
			fmt.Scanln(&t) //nolint:errcheck
		}

		c, conn, err := grpcDial()
		if err != nil {
			return err
		}
		defer closeConn(conn)

		r, err := c.Login(context.Background(), &pb.LoginRequest{Username: u, Password: p, TotpCode: t})
		if err != nil {
			return err
		}
		if err := os.MkdirAll(configDir, 0700); err != nil {
			return fmt.Errorf("mkdir config: %w", err)
		}
		if err := os.WriteFile(filepath.Join(configDir, "cert.pem"), []byte(r.ClientCert), 0600); err != nil {
			return fmt.Errorf("write cert: %w", err)
		}
		if err := os.WriteFile(filepath.Join(configDir, "key.pem"), []byte(r.ClientKey), 0600); err != nil {
			return fmt.Errorf("write key: %w", err)
		}
		saveCA(r.CaCert)
		fmt.Printf("✓ Logged in as %s (v%s)\n", r.Name, build.Version)
		return nil
	}}
	c.Flags().StringVarP(&u, "user", "u", "", "Username")
	c.Flags().StringVarP(&p, "pass", "p", "", "Password")
	c.Flags().StringVarP(&t, "totp", "t", "", "TOTP code")
	return c
}

func listC() *cobra.Command {
	return &cobra.Command{Use: "list", Short: "List challenges", RunE: func(cmd *cobra.Command, args []string) error {
		c, conn, err := grpcDial()
		if err != nil {
			return err
		}
		defer closeConn(conn)
		r, err := c.ListChallenges(context.Background(), &pb.ListChallengesRequest{})
		if err != nil {
			return err
		}
		for _, ch := range r.Challenges {
			badge := ""
			if ch.Active {
				badge = " [进行中]"
			} else if ch.Solved {
				badge = " ✓"
			}
			fmt.Printf("%-25s %-10s %-10s %s%s\n", ch.Id, ch.Type, ch.Difficulty, ch.Title, badge)
		}
		return nil
	}}
}

func startC() *cobra.Command {
	return &cobra.Command{Use: "start", Short: "Start a challenge and connect", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		challengeID := args[0]
		c, conn, err := grpcDial()
		if err != nil {
			return err
		}
		defer closeConn(conn)

		r, err := c.StartChallenge(context.Background(), &pb.StartChallengeRequest{ChallengeId: challengeID})
		if err != nil {
			return err
		}
		fmt.Printf("Challenge: %s\n", r.ChallengeTitle)

		return doExec(c, challengeID)
	}}
}

func resetC() *cobra.Command {
	return &cobra.Command{Use: "reset", Short: "Reset a challenge (destroy old, create new)", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		challengeID := args[0]
		c, conn, err := grpcDial()
		if err != nil {
			return err
		}
		defer closeConn(conn)

		r, err := c.ResetChallenge(context.Background(), &pb.ResetChallengeRequest{ChallengeId: challengeID})
		if err != nil {
			return err
		}
		fmt.Printf("Challenge: %s (fresh instance)\n", r.ChallengeTitle)

		return doExec(c, challengeID)
	}}
}

func subC() *cobra.Command {
	return &cobra.Command{Use: "submit", Short: "Submit solution", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		c, conn, err := grpcDial()
		if err != nil {
			return err
		}
		defer closeConn(conn)
		r, err := c.SubmitChallenge(context.Background(), &pb.SubmitChallengeRequest{ChallengeId: args[0]})
		if err != nil {
			return err
		}
		if r.Passed {
			fmt.Println("✓ PASSED!")
		} else {
			fmt.Printf("✗ FAILED (exit=%d)\n", r.ExitCode)
			if r.Output != "" {
				fmt.Println(r.Output)
			}
		}
		return nil
	}}
}

func genCmd() *cobra.Command {
	var topic string
	c := &cobra.Command{Use: "generate", Short: "Generate a challenge via agent", RunE: func(cmd *cobra.Command, args []string) error {
		c, conn, err := grpcDial()
		if err != nil {
			return err
		}
		defer closeConn(conn)
		fmt.Printf("Generating challenge for: %s\n(agent workflow may take 5-30 minutes)\n", topic)
		r, err := c.GenerateChallenge(context.Background(), &pb.GenerateChallengeRequest{Topic: topic})
		if err != nil {
			return err
		}
		fmt.Printf("Status: %s\n", r.Status)
		if r.ChallengeId != "" {
			fmt.Printf("Challenge: %s\n", r.ChallengeId)
		}
		if r.Detail != "" {
			fmt.Println(r.Detail)
		}
		return nil
	}}
	c.Flags().StringVar(&topic, "topic", "", "Challenge topic description")
	return c
}

// doExec opens a PTY session for the given challenge.
func doExec(client pb.BreakfixClient, challengeID string) error {
	stream, err := client.ExecInstance(context.Background())
	if err != nil {
		return fmt.Errorf("exec: %w", err)
	}
	stream.Send(&pb.PTYData{Data: []byte(challengeID)}) //nolint:errcheck

	if !term.IsTerminal(int(os.Stdin.Fd())) {
		fmt.Fprintln(os.Stderr, "stdin is not a terminal")
		return nil
	}

	oldState, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		return err
	}
	defer term.Restore(int(os.Stdin.Fd()), oldState) //nolint:errcheck

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGWINCH)
	defer signal.Stop(sigCh)

	go func() {
		if w, h, err := term.GetSize(int(os.Stdin.Fd())); err == nil {
			stream.Send(&pb.PTYData{Cols: uint32(w), Rows: uint32(h)}) //nolint:errcheck
		}
		for range sigCh {
			if w, h, err := term.GetSize(int(os.Stdin.Fd())); err == nil {
				stream.Send(&pb.PTYData{Cols: uint32(w), Rows: uint32(h)}) //nolint:errcheck
			}
		}
	}()

	// stdin → stream
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := os.Stdin.Read(buf)
			if n > 0 {
				stream.Send(&pb.PTYData{Data: buf[:n]}) //nolint:errcheck
			}
			if err != nil {
				stream.CloseSend() //nolint:errcheck
				return
			}
		}
	}()

	// stream → stdout
	for {
		data, err := stream.Recv()
		if err != nil {
			if err != io.EOF {
				fmt.Fprintf(os.Stderr, "\nDisconnected: %v\n", err)
			}
			return nil
		}
		if _, err := os.Stdout.Write(data.Data); err != nil {
			return fmt.Errorf("write: %w", err)
		}
	}
}
