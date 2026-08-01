// Package agentmodel centralizes the only model provider used by Breakfix.
package llm

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/breakfix/breakfix/internal/bootstrap/config"
	deepseek "github.com/cloudwego/eino-ext/components/model/deepseek"
	"github.com/cloudwego/eino/components/model"
	deepseekapi "github.com/cohesion-org/deepseek-go"
)

const maxTransportAttempts = 3

func NewChatModel(ctx context.Context, cfg config.AgentConfig) (model.ToolCallingChatModel, error) {
	if strings.TrimSpace(cfg.APIKey) == "" {
		return nil, fmt.Errorf("agent API key environment %q is empty", cfg.APIKeyEnv)
	}
	timeout, err := cfg.Timeout()
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(cfg.BaseURL) == "" || strings.TrimSpace(cfg.Model) == "" {
		return nil, errors.New("agent base_url and model are required")
	}
	chat, err := deepseek.NewChatModel(ctx, &deepseek.ChatModelConfig{
		APIKey:  cfg.APIKey,
		BaseURL: cfg.BaseURL,
		Model:   cfg.Model,
		Timeout: timeout,
	})
	if err != nil {
		return nil, fmt.Errorf("create deepseek chat model: %w", err)
	}
	return chat, nil
}

// RetryTransport retries only provider transport failures. It deliberately
// excludes model protocol, tool, 4xx parameter, and context-cancellation
// errors, which must become an attempt failure instead of a semantic result.
func RetryTransport(ctx context.Context, call func(context.Context) error) error {
	var last error
	for attempt := 0; attempt < maxTransportAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		last = call(ctx)
		if last == nil || !IsTransientTransportError(last) || attempt == maxTransportAttempts-1 {
			return last
		}
		delay := time.Duration(attempt+1) * 200 * time.Millisecond
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return last
}

func IsTransientTransportError(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var apiError *deepseekapi.APIError
	if errors.As(err, &apiError) {
		return apiError.StatusCode == http.StatusTooManyRequests || apiError.StatusCode >= 500
	}
	var netError net.Error
	return errors.As(err, &netError) && netError.Timeout()
}
