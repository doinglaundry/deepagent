package thread

import (
	"encoding/json"
	"fmt"

	"eino-cli/deepagent/manager/api"
	"eino-cli/deepagent/protocol"
	inputpkg "eino-cli/deepagent/protocol/input"
	corethread "eino-cli/deepagent/thread"
)

// transportInput translates only the managed Worker's wire protocol. Message
// parsing, identity, pending-input ownership and resume execution stay in Thread.
func transportInput(in protocol.Input, owner api.Thread) (*corethread.TransportMessage, error) {
	if err := in.Validate(); err != nil {
		return nil, err
	}
	if in.ThreadID != "" && in.ThreadID != owner.ID {
		return nil, fmt.Errorf("input belongs to another thread")
	}
	if in.SessionID != "" && in.SessionID != owner.SessionID {
		return nil, fmt.Errorf("input belongs to another session")
	}
	message := &corethread.TransportMessage{ID: in.ID, Sender: &corethread.TransportSender{Type: "user"}}
	var payload any
	switch in.Kind {
	case protocol.InputUser:
		user := inputpkg.UserMessage{}
		if owner.PlanMode {
			user.Mode = inputpkg.UserMessageModeImplPlan
		}
		if in.Text != "" {
			user.Parts = append(user.Parts, inputpkg.MessagePart{Type: inputpkg.MessagePartTypeText, Text: in.Text})
		}
		for _, part := range in.Parts {
			kind := inputpkg.MessagePartType(part.Type)
			switch part.Type {
			case "image_url":
				kind = inputpkg.MessagePartTypeImage
			case "audio_url":
				kind = inputpkg.MessagePartTypeAudio
			case "video_url":
				kind = inputpkg.MessagePartTypeVideo
			case "file_url":
				kind = inputpkg.MessagePartTypeFile
			case "text", "image", "audio", "video", "file":
			default:
				return nil, fmt.Errorf("unsupported message part %q", part.Type)
			}
			user.Parts = append(user.Parts, inputpkg.MessagePart{Type: kind, Text: part.Text, URL: part.URL, MIMEType: part.MIMEType})
		}
		message.Type = inputpkg.MessageTypeInput
		payload = user
	case protocol.InputCompact:
		message.Type = inputpkg.MessageTypeCompact
		payload = struct{}{}
	case protocol.InputResume:
		resume := in.Resume
		block := owner.Block
		kind := resume.Kind
		if block != nil {
			if block.RunID != resume.RunID || block.CheckpointID != resume.CheckpointID || block.InterruptID != resume.InterruptID {
				return nil, fmt.Errorf("resume does not match pending block")
			}
			kind = block.Kind
		}
		data := inputpkg.ResumeRunPayload{RunID: resume.RunID, CheckpointID: resume.CheckpointID, InterruptID: resume.InterruptID}
		if in.ID != "" {
			data.ConsumedMessageIDs = []string{in.ID}
		}
		switch kind {
		case "approval":
			data.Approval = &inputpkg.ApprovalDecision{Approved: resume.Approved, Reason: resume.Answer}
		case "clarification", "follow_up":
			answer, err := json.Marshal(map[string]string{"user_answer": resume.Answer})
			if err != nil {
				return nil, err
			}
			data.Interrupt = &inputpkg.InterruptResume{Kind: "follow_up", Data: answer}
		default:
			return nil, fmt.Errorf("unsupported block kind %q", kind)
		}
		message.Type = inputpkg.MessageTypeResume
		payload = data
	default:
		return nil, fmt.Errorf("control messages must be handled by managed Worker")
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	message.Payload = raw
	return message, nil
}
