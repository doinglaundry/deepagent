package input

import (
	"encoding/json"
	"errors"
	"strings"
)

const (
	MessageTypeInput   = "input"
	MessageTypeResume  = "resume_run"
	MessageTypeCompact = "compact"
	MetadataRunMode    = "run_mode"
	RunModePlan        = "plan"
)

type UserMessageMode string

const UserMessageModeImplPlan UserMessageMode = "plan"

type MessagePartType string

const (
	MessagePartTypeText  MessagePartType = "text"
	MessagePartTypeImage MessagePartType = "image"
	MessagePartTypeAudio MessagePartType = "audio"
	MessagePartTypeVideo MessagePartType = "video"
	MessagePartTypeFile  MessagePartType = "file"
)

type MessagePart struct {
	Type       MessagePartType            `json:"type"`
	Text       string                     `json:"text,omitempty"`
	URL        string                     `json:"url,omitempty"`
	MIMEType   string                     `json:"mime_type,omitempty"`
	Base64Data string                     `json:"base64_data,omitempty"`
	Detail     string                     `json:"detail,omitempty"`
	Name       string                     `json:"name,omitempty"`
	Extra      map[string]json.RawMessage `json:"extra,omitempty"`
}
type UserMessage struct {
	Mode  UserMessageMode            `json:"mode,omitempty"`
	Parts []MessagePart              `json:"parts"`
	Extra map[string]json.RawMessage `json:"extra,omitempty"`
}

func (m UserMessage) Validate() error {
	if len(m.Parts) == 0 {
		return errors.New("user message parts are required")
	}
	return nil
}

type ApprovalDecision struct {
	Approved       bool   `json:"approved"`
	AllowInSession bool   `json:"allow_in_session"`
	CancelRun      bool   `json:"cancel_run"`
	Reason         string `json:"reason,omitempty"`
}
type RequestUserInputAnswer struct {
	Answers []string `json:"answers"`
}
type RequestUserInputResponse struct {
	Answers map[string]RequestUserInputAnswer `json:"answers"`
}
type InterruptResume struct {
	Kind     string          `json:"kind"`
	InfoType string          `json:"info_type,omitempty"`
	Data     json.RawMessage `json:"data,omitempty"`
}
type ResumeRunPayload struct {
	RunID              string                    `json:"run_id"`
	CheckpointID       string                    `json:"checkpoint_id"`
	InterruptID        string                    `json:"interrupt_id"`
	Approval           *ApprovalDecision         `json:"approval,omitempty"`
	RequestUserInput   *RequestUserInputResponse `json:"request_user_input,omitempty"`
	Interrupt          *InterruptResume          `json:"interrupt,omitempty"`
	ConsumedMessageIDs []string                  `json:"consumed_message_ids,omitempty"`
}

func (p ResumeRunPayload) Validate() error {
	if strings.TrimSpace(p.RunID) == "" || strings.TrimSpace(p.CheckpointID) == "" || strings.TrimSpace(p.InterruptID) == "" {
		return errors.New("run_id, checkpoint_id and interrupt_id are required")
	}
	return nil
}
