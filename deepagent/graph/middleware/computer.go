package middleware

import (
	"context"
	"strings"

	"eino-cli/deepagent/graph/computer"
	agentmodel "eino-cli/deepagent/model"

	"github.com/cloudwego/eino/schema"
)

// Computer releases this Run's desktop lease and trims only request screenshots.
// Screens and observations remain in history; this middleware creates no tools.
type Computer struct {
	BaseMiddleware
	desktop    *computer.Desktop
	ownerRunID string
}

func NewComputer(desktop *computer.Desktop) agentmodel.Middleware { return &Computer{desktop: desktop} }
func (*Computer) GetName() string                                 { return "computer" }
func (middleware *Computer) NewRun() agentmodel.Middleware        { return NewComputer(middleware.desktop) }
func (middleware *Computer) PrepareRun(_ context.Context, runState *agentmodel.RunState) error {
	if runState.Depth == 0 {
		middleware.ownerRunID = runState.RunID
	}
	return nil
}
func (*Computer) FinishRun(context.Context, *agentmodel.RunState, error) error { return nil }
func (middleware *Computer) Close(ctx context.Context) error {
	if middleware.desktop == nil || middleware.ownerRunID == "" {
		return nil
	}
	ownerRunID := middleware.ownerRunID
	middleware.ownerRunID = ""
	_, err := middleware.desktop.PerformAction(ctx, ownerRunID, "release", agentmodel.ComputerAction{})
	return err
}
func (*Computer) ModifyModelRequest(_ context.Context, _ []*agentmodel.Message, messages []*agentmodel.Message, _ *agentmodel.GraphState) ([]*agentmodel.Message, error) {
	request := append([]*agentmodel.Message(nil), messages...)
	keepLatestScreenshot := true
	for index := len(messages) - 1; index >= 0; index-- {
		message := messages[index]
		if message == nil || message.Role != schema.Tool {
			continue
		}
		if !strings.HasPrefix(message.ToolName, "browser_") && !strings.HasPrefix(message.ToolName, "computer_") {
			continue
		}
		if keepLatestScreenshot {
			for _, part := range message.UserInputMultiContent {
				if part.Type == schema.ChatMessagePartTypeImageURL {
					keepLatestScreenshot = false
					break
				}
			}
			continue
		}
		// 只裁剪模型请求里的旧截图，历史消息保持不变。
		toolMessage := *message
		toolMessage.UserInputMultiContent = nil
		for _, part := range message.UserInputMultiContent {
			if part.Type != schema.ChatMessagePartTypeImageURL {
				toolMessage.UserInputMultiContent = append(toolMessage.UserInputMultiContent, part)
			}
		}
		request[index] = &toolMessage
	}
	return request, nil
}
