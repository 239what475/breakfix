package draftreview

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	claudecode "github.com/239what475/eino-claude-code"
	breakfixv1 "github.com/breakfix/breakfix/apis/breakfix/v1"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
	"log/slog"
)

type ReviewResult struct {
	Verdict  string                    `json:"verdict"`
	Reason   string                    `json:"reason"`
	Warnings []string                  `json:"warnings"`
	Draft    breakfixv1.ChallengeDraft `json:"draft"`
}

type Reviewer struct{}

func (r *Reviewer) Review(ctx context.Context, topic string) (*ReviewResult, error) {
	sessionID := claudecode.NewSessionID()
	prompt := userPrompt(topic)
	firstTurn := true

	for {
		result, err := runReviewTurn(ctx, sessionID, prompt, firstTurn)
		if err != nil {
			return nil, err
		}

		if err := validateReviewResult(result); err != nil {
			prompt = retryPrompt(err)
			firstTurn = false
			continue
		}
		return result, nil
	}
}

func runReviewTurn(ctx context.Context, sessionID, prompt string, firstTurn bool) (*ReviewResult, error) {
	opts := []claudecode.Option{
		claudecode.WithSystemPrompt(systemPrompt()),
		claudecode.WithTools(),
		claudecode.WithPermissionMode("default"),
		claudecode.WithStderr(func(line string) { slog.Debug("draft-review", "msg", line) }),
	}
	if firstTurn {
		opts = append(opts, claudecode.WithSessionID(sessionID))
	} else {
		opts = append(opts, claudecode.WithResume(sessionID))
	}

	agent, err := claudecode.New(opts...)
	if err != nil {
		return nil, fmt.Errorf("create review agent: %w", err)
	}

	runner := adk.NewRunner(ctx, adk.RunnerConfig{Agent: agent})
	events := runner.Run(ctx, []adk.Message{schema.UserMessage(prompt)})

	var lastMsg string
	for {
		evt, ok := events.Next()
		if !ok {
			break
		}
		if evt.Err != nil {
			return nil, evt.Err
		}
		if evt.Output != nil && evt.Output.MessageOutput != nil && evt.Output.MessageOutput.Message != nil {
			msg := evt.Output.MessageOutput.Message
			if c := strings.TrimSpace(msg.Content); c != "" {
				lastMsg = c
			}
		}
		if evt.Action != nil && evt.Action.Exit {
			break
		}
	}

	lastMsg = extractJSON(lastMsg)
	var result ReviewResult
	if err := json.Unmarshal([]byte(lastMsg), &result); err != nil {
		return nil, fmt.Errorf("parse review result: %w", err)
	}
	return &result, nil
}

func validateReviewResult(result *ReviewResult) error {
	if result == nil {
		return fmt.Errorf("结果为空")
	}

	switch result.Verdict {
	case "good", "weak", "unsafe", "too-vague":
	default:
		return fmt.Errorf("verdict 不合法，必须是 good、weak、unsafe、too-vague 之一")
	}

	if strings.TrimSpace(result.Reason) == "" {
		return fmt.Errorf("reason 不能为空")
	}

	draft := result.Draft
	if strings.TrimSpace(draft.Title) == "" {
		return fmt.Errorf("draft.title 不能为空")
	}
	switch draft.Difficulty {
	case "easy", "medium", "hard":
	default:
		return fmt.Errorf("draft.difficulty 不合法，必须是 easy、medium、hard 之一")
	}
	if len(draft.Tags) == 0 {
		return fmt.Errorf("draft.tags 不能为空")
	}
	for i, tag := range draft.Tags {
		if strings.TrimSpace(tag) == "" {
			return fmt.Errorf("draft.tags[%d] 不能为空", i)
		}
	}
	if strings.TrimSpace(draft.Description) == "" {
		return fmt.Errorf("draft.description 不能为空")
	}
	if strings.TrimSpace(draft.Goal) == "" {
		return fmt.Errorf("draft.goal 不能为空")
	}
	if strings.TrimSpace(draft.Symptoms) == "" {
		return fmt.Errorf("draft.symptoms 不能为空")
	}
	if strings.TrimSpace(draft.FaultMechanism) == "" {
		return fmt.Errorf("draft.fault_mechanism 不能为空")
	}
	if strings.TrimSpace(draft.EnvironmentShape) == "" {
		return fmt.Errorf("draft.environment_shape 不能为空")
	}
	if strings.TrimSpace(draft.AcceptanceCriteria) == "" {
		return fmt.Errorf("draft.acceptance_criteria 不能为空")
	}
	if strings.TrimSpace(draft.DifficultyReason) == "" {
		return fmt.Errorf("draft.difficulty_reason 不能为空")
	}

	return nil
}

func retryPrompt(err error) string {
	return fmt.Sprintf(`你刚才输出的 JSON 不符合要求，需要在同一个会话里直接重新生成一份完整、合法的结果。

本次失败原因：
%s

要求：
1. 只返回 JSON，不要输出解释。
2. 保持原题意，不要偏离用户最初的题目想法。
3. 所有必填字段都必须完整、非空、合法。
4. difficulty 只能是 easy、medium、hard。
5. verdict 只能是 good、weak、unsafe、too-vague。`, err.Error())
}

func extractJSON(raw string) string {
	start := strings.Index(raw, "{")
	end := strings.LastIndex(raw, "}")
	if start >= 0 && end > start {
		return raw[start : end+1]
	}
	return raw
}

func systemPrompt() string {
	return `你是 Breakfix 平台的高级 SRE 题目策划者。

你的任务不是生成脚本、镜像或文件。
你的任务是把一个粗略的题目想法，整理成一个“边界清晰、目标明确、可验证、但保留探索空间”的题目草案。

你输出的草案必须足够清晰，使后续生成器能够构建题目环境，而不是自行发散增加无关复杂度。

只返回 JSON，不要输出 markdown，不要输出额外解释。

输出结构：
{
  "verdict": "good" | "weak" | "unsafe" | "too-vague",
  "reason": "一句简短判断理由",
  "warnings": ["提醒1", "提醒2"],
  "draft": {
    "title": "题目标题",
    "difficulty": "easy|medium|hard",
    "tags": ["标签1", "标签2"],
    "description": "给用户看的简短中文说明",
    "goal": "用户最终需要让环境达到什么目标状态",
    "symptoms": "用户在初始状态下能观察到什么现象、报错、异常行为",
    "fault_mechanism": "这道题底层到底是什么类型的故障机制。描述机制，不要直接给唯一解法。",
    "environment_shape": "这道题需要什么样的环境形态，例如涉及哪些服务、文件、进程、目录、工具或系统能力",
    "acceptance_criteria": "题目通过时必须满足哪些可验证的结果。必须能拆成公开检查点验证的结果，而不是模糊描述。",
    "difficulty_reason": "为什么这道题属于这个难度",
    "notes": "给后续生成器的补充说明。只写真正有帮助的实现提示，不要写长故事。"
  }
}

强约束：
1. draft.title、draft.difficulty、draft.tags、draft.description、draft.goal、draft.symptoms、draft.fault_mechanism、draft.environment_shape、draft.acceptance_criteria、draft.difficulty_reason 都必须存在且非空。
2. verdict 必须是 good、weak、unsafe、too-vague 之一。
3. difficulty 必须是 easy、medium、hard 之一。
4. 如果你发现自己漏了字段，必须自行补全后再输出，不能省略。

要求：
1. 优先设计真实的 Linux / SRE / 基础设施排障题，而不是纯知识问答或纯配置填空。
2. 题目必须保留用户的探索空间，不要直接规定用户必须运行哪些命令，也不要直接规定必须修改哪个文件的哪一行，除非这是题目本身不可避免的一部分。
3. 必须明确：
   - 用户目标是什么
   - 初始可观察现象是什么
   - 底层故障机制是什么
   - 最终通过条件是什么
4. acceptance_criteria 必须是具体的、可机器验证的结果，而不是“系统恢复正常”“服务可用”这种模糊描述。
5. 不要把题目扩展成失控的多层复杂故事。除非难度确实需要，否则应避免多个彼此独立的主故障。
6. environment_shape 必须足够清楚，避免后续生成器自行脑补不受控的系统结构。
7. 如果原始想法太弱、太空、太像故事、太难验证，你应该把它收紧，而不是把它写得更大更散。
8. description 用中文简洁表达；其余字段也用中文表达，但要准确、工程化、避免文学化。
9. 如果题目存在高风险、不现实、不可验证、或过度模糊的问题，应在 verdict 和 warnings 中明确指出。`
}

func userPrompt(topic string) string {
	return fmt.Sprintf(`请把下面这个题目想法整理成 Breakfix 平台可用的题目草案。

重点不是把故事写长，而是把下面这些东西写清楚：
1. 用户最终目标是什么
2. 用户一开始能观察到什么症状
3. 题目底层的故障机制是什么
4. 这道题需要什么样的环境形态
5. 最终通过条件应该如何被明确验证
6. 这道题为什么属于对应难度

题目想法：
%s`, topic)
}
