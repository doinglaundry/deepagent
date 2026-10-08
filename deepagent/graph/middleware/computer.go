package middleware

import (
	"context"
	"strings"

	"eino-cli/deepagent/graph/computer"
	"eino-cli/deepagent/graph/types"
	messagepkg "eino-cli/deepagent/message"

	"github.com/cloudwego/eino/schema"
)

// Computer releases this Run's desktop lease and trims only request screenshots.
// Screens and observations remain in history; this middleware creates no tools.
type Computer struct {
	BaseMiddleware
	desktop    *computer.Desktop
	ownerRunID string
}

func NewComputer(desktop *computer.Desktop) Middleware { return &Computer{desktop: desktop} }
func (*Computer) GetName() string                      { return "computer" }
func (middleware *Computer) NewRun() Middleware        { return NewComputer(middleware.desktop) }
func (middleware *Computer) PrepareRun(_ context.Context, runState *types.RunState) error {
	if runState.Depth == 0 {
		middleware.ownerRunID = runState.RunID
	}
	return nil
}
func (*Computer) FinishRun(context.Context, *types.RunState, error) error { return nil }
func (middleware *Computer) Close(ctx context.Context) error {
	if middleware.desktop == nil || middleware.ownerRunID == "" {
		return nil
	}
	ownerRunID := middleware.ownerRunID
	middleware.ownerRunID = ""
	_, err := middleware.desktop.PerformAction(ctx, ownerRunID, "release", computer.Action{})
	return err
}
func (*Computer) ModifyModelRequest(_ context.Context, _ []*messagepkg.Message, messages []*messagepkg.Message, _ *types.GraphState) ([]*messagepkg.Message, error) {
	request := append([]*messagepkg.Message(nil), messages...)
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
