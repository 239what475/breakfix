package generator

import (
	"log/slog"

	"github.com/cloudwego/eino/adk"
)

// drainEvents processes agent events without emitting model content, reasoning,
// tool arguments, or tool results to logs.
func (g *Generator) drainEvents(events *adk.AsyncIterator[*adk.AgentEvent], onEvent func()) error {
	for {
		evt, ok := events.Next()
		if !ok {
			return nil
		}
		if onEvent != nil {
			onEvent()
		}
		if evt.Err != nil {
			slog.Error("agent error", "err", evt.Err)
			return evt.Err
		}
		if evt.Output == nil || evt.Output.MessageOutput == nil || evt.Output.MessageOutput.Message == nil {
			if evt.Action != nil && evt.Action.Exit {
				return nil
			}
			continue
		}
		msg := evt.Output.MessageOutput.Message
		for _, tc := range msg.ToolCalls {
			slog.Debug("agent tool call", "tool", tc.Function.Name)
		}
		if evt.Action != nil && evt.Action.Exit {
			return nil
		}
	}
}

func truncateStr(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
