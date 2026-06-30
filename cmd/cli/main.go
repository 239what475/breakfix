package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/term"

	"github.com/breakfix/breakfix/internal/build"
	"github.com/gorilla/websocket"
	"github.com/spf13/cobra"
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

func apiURL(path string) string {
	return fmt.Sprintf("http://%s:9090/api%s", serverAddr, path)
}

func token() string {
	data, _ := os.ReadFile(filepath.Join(configDir, "token"))
	return strings.TrimSpace(string(data))
}

func httpDo(method, path string, body, result interface{}) error {
	var r io.Reader
	if body != nil {
		data, _ := json.Marshal(body)
		r = bytes.NewReader(data)
	}

	req, _ := http.NewRequest(method, apiURL(path), r)
	if r != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if tok := token(); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		var e struct{ Error string }
		json.NewDecoder(resp.Body).Decode(&e)
		return fmt.Errorf("%s", e.Error)
	}

	if result != nil {
		return json.NewDecoder(resp.Body).Decode(result)
	}
	return nil
}

func regCmd() *cobra.Command {
	var u, p string
	c := &cobra.Command{Use: "register", Short: "Register", RunE: func(cmd *cobra.Command, args []string) error {
		var result struct {
			TotpSecret string `json:"totp_secret"`
			TotpUrl    string `json:"totp_url"`
		}
		if err := httpDo("POST", "/auth/register", map[string]string{
			"username": u, "password": p,
		}, &result); err != nil {
			return err
		}

		renderQR(result.TotpUrl)

		if err := os.MkdirAll(configDir, 0700); err != nil {
			return fmt.Errorf("mkdir config: %w", err)
		}
		if err := os.WriteFile(filepath.Join(configDir, "totp-secret"), []byte(result.TotpSecret), 0600); err != nil {
			return fmt.Errorf("write totp: %w", err)
		}
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
		if t == "" {
			fmt.Print("Enter TOTP code: ")
			fmt.Scanln(&t) //nolint:errcheck
		}

		var result struct {
			Token  string `json:"token"`
			UserId string `json:"user_id"`
			Name   string `json:"name"`
		}
		if err := httpDo("POST", "/auth/login", map[string]string{
			"username": u, "password": p, "totp_code": t,
		}, &result); err != nil {
			return err
		}

		if err := os.MkdirAll(configDir, 0700); err != nil {
			return fmt.Errorf("mkdir config: %w", err)
		}
		if err := os.WriteFile(filepath.Join(configDir, "token"), []byte(result.Token), 0600); err != nil {
			return fmt.Errorf("write token: %w", err)
		}
		fmt.Printf("✓ Logged in as %s (v%s)\n", result.Name, build.Version)
		return nil
	}}
	c.Flags().StringVarP(&u, "user", "u", "", "Username")
	c.Flags().StringVarP(&p, "pass", "p", "", "Password")
	c.Flags().StringVarP(&t, "totp", "t", "", "TOTP code")
	return c
}

func listC() *cobra.Command {
	return &cobra.Command{Use: "list", Short: "List challenges", RunE: func(cmd *cobra.Command, args []string) error {
		var result struct {
			Challenges []struct {
				Id         string   `json:"id"`
				Title      string   `json:"title"`
				Type       string   `json:"type"`
				Difficulty string   `json:"difficulty"`
				Tags       []string `json:"tags"`
				Solved     bool     `json:"solved"`
				Active     bool     `json:"active"`
			} `json:"challenges"`
		}
		if err := httpDo("GET", "/challenges", nil, &result); err != nil {
			return err
		}
		for _, ch := range result.Challenges {
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
		var result struct {
			ChallengeTitle string `json:"challenge_title"`
		}
		if err := httpDo("POST", "/challenges/"+challengeID+"/start", nil, &result); err != nil {
			return err
		}
		fmt.Printf("Challenge: %s\n", result.ChallengeTitle)
		return doTerminal(challengeID)
	}}
}

func resetC() *cobra.Command {
	return &cobra.Command{Use: "reset", Short: "Reset a challenge", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		challengeID := args[0]
		var result struct {
			ChallengeTitle string `json:"challenge_title"`
		}
		if err := httpDo("POST", "/challenges/"+challengeID+"/reset", nil, &result); err != nil {
			return err
		}
		fmt.Printf("Challenge: %s (fresh instance)\n", result.ChallengeTitle)
		return doTerminal(challengeID)
	}}
}

func subC() *cobra.Command {
	return &cobra.Command{Use: "submit", Short: "Submit solution", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		var result struct {
			Passed   bool   `json:"passed"`
			ExitCode int    `json:"exit_code"`
			Output   string `json:"output"`
		}
		if err := httpDo("POST", "/challenges/"+args[0]+"/submit", nil, &result); err != nil {
			return err
		}
		if result.Passed {
			fmt.Println("✓ PASSED!")
		} else {
			fmt.Printf("✗ FAILED (exit=%d)\n", result.ExitCode)
			if result.Output != "" {
				fmt.Println(result.Output)
			}
		}
		return nil
	}}
}

func genCmd() *cobra.Command {
	var topic string
	c := &cobra.Command{Use: "generate", Short: "Generate a challenge via agent", RunE: func(cmd *cobra.Command, args []string) error {
		fmt.Printf("Generating challenge for: %s\n(agent workflow may take 5-30 minutes)\n", topic)
		var result struct {
			ChallengeId string `json:"challenge_id"`
			Status      string `json:"status"`
			Detail      string `json:"detail"`
		}
		if err := httpDo("POST", "/generate", map[string]string{"topic": topic}, &result); err != nil {
			return err
		}
		fmt.Printf("Status: %s\n", result.Status)
		if result.ChallengeId != "" {
			fmt.Printf("Challenge: %s\n", result.ChallengeId)
		}
		if result.Detail != "" {
			fmt.Println(result.Detail)
		}
		return nil
	}}
	c.Flags().StringVar(&topic, "topic", "", "Challenge topic description")
	return c
}

// doTerminal opens a WebSocket PTY session for the given challenge.
func doTerminal(challengeID string) error {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		fmt.Fprintln(os.Stderr, "stdin is not a terminal")
		return nil
	}

	wsURL := fmt.Sprintf("ws://%s:9090/api/challenges/%s/terminal", serverAddr, challengeID)
	header := http.Header{}
	if tok := token(); tok != "" {
		header.Set("Authorization", "Bearer "+tok)
	}

	conn, _, err := websocket.DefaultDialer.Dial(wsURL, header)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer conn.Close()

	oldState, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		return err
	}
	defer term.Restore(int(os.Stdin.Fd()), oldState) //nolint:errcheck

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGWINCH)
	defer signal.Stop(sigCh)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Resize events
	go func() {
		sendResize(conn)
		for {
			select {
			case <-sigCh:
				sendResize(conn)
			case <-ctx.Done():
				return
			}
		}
	}()

	// stdin → ws
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := os.Stdin.Read(buf)
			if err != nil {
				cancel()
				return
			}
			msg, _ := json.Marshal(map[string]interface{}{
				"type": "data",
				"data": buf[:n],
			})
			conn.WriteMessage(websocket.TextMessage, msg)
		}
	}()

	// ws → stdout
	for {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			return nil
		}
		var m struct {
			Type string `json:"type"`
			Data []byte `json:"data"`
		}
		if json.Unmarshal(msg, &m) == nil && m.Type == "data" {
			os.Stdout.Write(m.Data)
		}
	}
}

func sendResize(conn *websocket.Conn) {
	if w, h, err := term.GetSize(int(os.Stdin.Fd())); err == nil {
		msg, _ := json.Marshal(map[string]interface{}{
			"type": "resize",
			"cols": w,
			"rows": h,
		})
		conn.WriteMessage(websocket.TextMessage, msg)
	}
}

// Keep QR code rendering from original
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

	qzRow := strings.Repeat(qz, scale+4)

	for i := 0; i < 2; i++ {
		sb.WriteString(qzRow)
		sb.WriteByte('\n')
	}
	for y := 0; y < scale; y++ {
		sb.WriteString(strings.Repeat(qz, 2))
		for x := 0; x < scale; x++ {
			if code.Black(x, y) {
				sb.WriteString(bgBlack)
			} else {
				sb.WriteString(bgWhite)
			}
		}
		sb.WriteString(strings.Repeat(qz, 2))
		sb.WriteByte('\n')
	}
	for i := 0; i < 2; i++ {
		sb.WriteString(qzRow)
		sb.WriteByte('\n')
	}

	fmt.Print(sb.String())
}

