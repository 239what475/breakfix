package llm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	assistant "github.com/breakfix/breakfix/internal/application/assistant"
	"github.com/breakfix/breakfix/internal/config"
	"github.com/breakfix/breakfix/internal/domain/agent"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

const (
	maxAssistantTransportAttempts = 3
	maxAssistantTurns             = 12
)

// AssistantExecutor is the Eino implementation of the assistant application
// port. It owns model construction and transport retry behavior only.
type AssistantExecutor struct {
	config config.AgentConfig
}

func NewAssistantExecutor(cfg config.AgentConfig) *AssistantExecutor {
	return &AssistantExecutor{config: cfg}
}

func (e *AssistantExecutor) Run(ctx context.Context, request assistant.Request, history []agent.Message, emit func(assistant.StreamEvent)) (assistant.EngineResult, error) {
	if err := assistant.ValidateRequest(request); err != nil {
		return assistant.EngineResult{}, err
	}
	if len(history) == 0 || history[len(history)-1].Role != "user" {
		return assistant.EngineResult{}, errors.New("assistant execution requires a latest user message")
	}
	for attempt := 0; attempt < maxAssistantTransportAttempts; attempt++ {
		result, err := runAssistantAttempt(ctx, e.config, request, history, emit)
		if err == nil {
			return result, nil
		}
		if !IsTransientTransportError(err) || attempt == maxAssistantTransportAttempts-1 {
			return assistant.EngineResult{}, err
		}
		if emit != nil {
			emit(assistant.StreamEvent{Type: "reset"})
		}
		if err := waitAssistantTransportRetry(ctx, attempt); err != nil {
			return assistant.EngineResult{}, err
		}
	}
	return assistant.EngineResult{}, errors.New("assistant transport retry exhausted")
}

func runAssistantAttempt(ctx context.Context, cfg config.AgentConfig, request assistant.Request, history []agent.Message, emit func(assistant.StreamEvent)) (assistant.EngineResult, error) {
	chat, err := NewChatModel(ctx, cfg)
	if err != nil {
		return assistant.EngineResult{}, err
	}
	conversation := &conversation{request: request, emit: emit}
	inputs, err := assistantInputs(conversation, history)
	if err != nil {
		return assistant.EngineResult{}, err
	}
	agent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:          "challenge_assistant",
		Description:   "Breakfix challenge learning assistant",
		Instruction:   assistantSystemPrompt(),
		Model:         chat,
		MaxIterations: maxAssistantTurns,
		ToolsConfig: adk.ToolsConfig{ToolsNodeConfig: compose.ToolsNodeConfig{
			Tools: toBaseTools(conversation.tools()),
		}},
	})
	if err != nil {
		return assistant.EngineResult{}, fmt.Errorf("create Eino assistant agent: %w", err)
	}

	runner := adk.NewRunner(ctx, adk.RunnerConfig{Agent: agent, EnableStreaming: true})
	events := runner.Run(ctx, inputs)
	var response strings.Builder
	for {
		event, ok := events.Next()
		if !ok {
			break
		}
		if event.Err != nil {
			return assistant.EngineResult{}, event.Err
		}
		if event.Output != nil && event.Output.MessageOutput != nil {
			output := event.Output.MessageOutput
			if output.IsStreaming && output.MessageStream != nil {
				stream := output.MessageStream
				for {
					chunk, streamErr := stream.Recv()
					if errors.Is(streamErr, io.EOF) {
						break
					}
					if streamErr != nil {
						stream.Close()
						return assistant.EngineResult{}, fmt.Errorf("read Eino assistant stream: %w", streamErr)
					}
					if chunk == nil || chunk.Content == "" {
						continue
					}
					response.WriteString(chunk.Content)
					conversation.notify(assistant.StreamEvent{Type: "delta", Content: chunk.Content})
				}
				stream.Close()
			}
			if output.Message != nil {
				for _, call := range output.Message.ToolCalls {
					conversation.notify(assistant.StreamEvent{Type: "tool", Tool: call.Function.Name})
				}
				if content := output.Message.Content; content != "" {
					response.WriteString(content)
					conversation.notify(assistant.StreamEvent{Type: "delta", Content: content})
				}
			}
		}
		if event.Action != nil && event.Action.Exit {
			break
		}
	}
	content := strings.TrimSpace(response.String())
	if content == "" {
		return assistant.EngineResult{}, errors.New("assistant returned an empty response")
	}
	return assistant.EngineResult{Content: content, Evidence: conversation.evidence()}, nil
}

func waitAssistantTransportRetry(ctx context.Context, attempt int) error {
	delay := time.Duration(attempt+1) * 200 * time.Millisecond
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func assistantInputs(conversation *conversation, history []agent.Message) ([]adk.Message, error) {
	inputs := make([]adk.Message, 0, len(history))
	for index, message := range history {
		switch message.Role {
		case "user":
			content := message.Content
			if index == len(history)-1 {
				var err error
				content, err = conversation.prompt(message.Content)
				if err != nil {
					return nil, err
				}
			}
			inputs = append(inputs, schema.UserMessage(content))
		case "assistant":
			inputs = append(inputs, schema.AssistantMessage(message.Content, nil))
		default:
			return nil, fmt.Errorf("unsupported assistant history role %q", message.Role)
		}
	}
	return inputs, nil
}

func toBaseTools(values []tool.InvokableTool) []tool.BaseTool {
	result := make([]tool.BaseTool, 0, len(values))
	for _, value := range values {
		result = append(result, value)
	}
	return result
}
