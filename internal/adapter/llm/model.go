// Package agentmodel centralizes the only model provider used by Breakfix.
package llm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"

	"github.com/breakfix/breakfix/internal/bootstrap/config"
	deepseek "github.com/cloudwego/eino-ext/components/model/deepseek"
	"github.com/cloudwego/eino/components/model"
	deepseekapi "github.com/cohesion-org/deepseek-go"
)

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

func IsTransientTransportError(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	if errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	var apiError *deepseekapi.APIError
	if errors.As(err, &apiError) {
		return apiError.StatusCode == http.StatusTooManyRequests || apiError.StatusCode >= 500
	}
	var netError net.Error
	return errors.As(err, &netError) && netError.Timeout()
}
