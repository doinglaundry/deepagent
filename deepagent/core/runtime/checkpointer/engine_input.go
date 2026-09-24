package checkpointer

import (
	"eino-cli/deepagent/protocol"
	"fmt"
	"github.com/cloudwego/eino/schema"
)

// Decode the old checkpoint's transport input at the migration boundary.
// Both IDs are retained: old history dedup and the current Thread adapter.
func engineInput(in protocol.Input, threadID string) (*schema.Message, error) {
	if in.ID == "" || in.Kind != protocol.InputUser || (in.ThreadID != "" && in.ThreadID != threadID) {
		return nil, fmt.Errorf("invalid legacy pending input identity")
	}
	if err := in.Validate(); err != nil {
		return nil, err
	}
	message := schema.UserMessage(in.Text)
	message.Extra = map[string]any{"deepagent_input_id": in.ID, "message_id": in.ID}
	if len(in.Parts) > 0 && in.Text != "" {
		message.UserInputMultiContent = append(message.UserInputMultiContent, schema.MessageInputPart{Type: schema.ChatMessagePartTypeText, Text: in.Text})
	}
	for _, p := range in.Parts {
		common := schema.MessagePartCommon{URL: &p.URL, MIMEType: p.MIMEType}
		part := schema.MessageInputPart{}
		switch p.Type {
		case "image", "image_url":
			part.Type = schema.ChatMessagePartTypeImageURL
			part.Image = &schema.MessageInputImage{MessagePartCommon: common}
		case "audio", "audio_url":
			part.Type = schema.ChatMessagePartTypeAudioURL
			part.Audio = &schema.MessageInputAudio{MessagePartCommon: common}
		case "video", "video_url":
			part.Type = schema.ChatMessagePartTypeVideoURL
			part.Video = &schema.MessageInputVideo{MessagePartCommon: common}
		case "file", "file_url":
			part.Type = schema.ChatMessagePartTypeFileURL
			part.File = &schema.MessageInputFile{MessagePartCommon: common}
		case "text":
			part.Type = schema.ChatMessagePartTypeText
			part.Text = p.Text
			if message.Content != "" && p.Text != "" {
				message.Content += "\n"
			}
			message.Content += p.Text
		default:
			return nil, fmt.Errorf("unsupported legacy pending input part %q", p.Type)
		}
		message.UserInputMultiContent = append(message.UserInputMultiContent, part)
	}
	return message, nil
}
