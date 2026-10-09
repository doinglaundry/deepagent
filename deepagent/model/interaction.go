package model

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/cloudwego/eino/schema"
)

// ApprovalRememberer records a session-scoped approval reuse decision.
type ApprovalRememberer interface {
	RememberApproval(ctx context.Context, payload ResumeRunPayload)
}

type ApprovalRemembererFunc func(ctx context.Context, payload ResumeRunPayload)

// InterruptResumeDecoder converts a generic Run interrupt resume payload
// into the typed data expected by a custom Eino interrupt handler.
type InterruptResumeDecoder func(ctx context.Context, payload ResumeRunPayload) (any, error)

// InterruptOptions describes one external request to interrupt the active Run.
//
// Metadata is intentionally opaque to  Worker/control-plane hosts
// may use it to correlate the surfaced external interrupt with their own
// protocol concepts, while the runner only passes it through to events.
type InterruptOptions struct {
	Timeout  *time.Duration
	Metadata map[string]string
}

type FollowUpInfo struct {
	Question, UserAnswer string
	Questions            []string
}

type PlanUpdate struct {
	Plan        []PlanStep `json:"plan"`
	Explanation string     `json:"explanation,omitempty"`
}

type PlanUpdateHandler func(context.Context, PlanUpdate) error

type Interrupt struct {
	InterruptID  string
	CallID       string
	CheckpointID string
	Kind         string
	Data         json.RawMessage
}

type RequestUserInputOption struct{ Label, Description string }

type RequestUserInputQuestion struct {
	ID, Header, Question string
	Options              []RequestUserInputOption
}

type RequestUserInputInfo struct{ Questions []RequestUserInputQuestion }

type RequestUserInputAnswer struct{ Answers []string }

type RequestUserInputResponse struct {
	Answers map[string]RequestUserInputAnswer
}

type ApprovalDecision struct {
	Approved bool `json:"approved"`
	// AlwaysAllow remembers this tool for this Thread, including future Runs.
	AlwaysAllow    bool   `json:"always_allow,omitempty"`
	AllowInSession bool   `json:"allow_in_session"`
	CancelRun      bool   `json:"cancel_run"`
	Reason         string `json:"reason,omitempty"`
}

type InputRequestUserInputAnswer struct {
	Answers []string `json:"answers"`
}

type InputRequestUserInputResponse struct {
	Answers map[string]InputRequestUserInputAnswer `json:"answers"`
}

type InterruptResume struct {
	Kind     string          `json:"kind"`
	InfoType string          `json:"info_type,omitempty"`
	Data     json.RawMessage `json:"data,omitempty"`
}

type ResumeRunPayload struct {
	RunID              string                         `json:"run_id"`
	CheckpointID       string                         `json:"checkpoint_id"`
	InterruptID        string                         `json:"interrupt_id"`
	Approval           *ApprovalDecision              `json:"approval,omitempty"`
	RequestUserInput   *InputRequestUserInputResponse `json:"request_user_input,omitempty"`
	Interrupt          *InterruptResume               `json:"interrupt,omitempty"`
	Answers            []ResumeAnswer                 `json:"answers,omitempty"`
	ConsumedMessageIDs []string                       `json:"consumed_message_ids,omitempty"`
}

type ResumeAnswer struct {
	InterruptID      string                         `json:"interrupt_id"`
	Approval         *ApprovalDecision              `json:"approval,omitempty"`
	RequestUserInput *InputRequestUserInputResponse `json:"request_user_input,omitempty"`
	Interrupt        *InterruptResume               `json:"interrupt,omitempty"`
}

func (f ApprovalRemembererFunc) RememberApproval(ctx context.Context, payload ResumeRunPayload) {
	f(ctx, payload)
}

func init() {
	schema.RegisterName[*FollowUpInfo]("deepagent_follow_up_info")
}

func (p ResumeRunPayload) Validate() error {
	if strings.TrimSpace(p.RunID) == "" || strings.TrimSpace(p.CheckpointID) == "" || strings.TrimSpace(p.InterruptID) == "" {
		return errors.New("run_id, checkpoint_id and interrupt_id are required")
	}
	if len(p.Answers) > 0 {
		if p.Answers[0].InterruptID != p.InterruptID || p.Approval != nil || p.RequestUserInput != nil || p.Interrupt != nil {
			return errors.New("batch answers must start with interrupt_id and cannot mix with a single answer")
		}
		seen := map[string]bool{}
		for _, answer := range p.Answers {
			if answer.InterruptID == "" || seen[answer.InterruptID] {
				return errors.New("batch answer interrupt IDs must be nonempty and unique")
			}
			seen[answer.InterruptID] = true
			count := 0
			if answer.Approval != nil {
				count++
			}
			if answer.RequestUserInput != nil {
				count++
			}
			if answer.Interrupt != nil {
				count++
			}
			if count != 1 {
				return errors.New("each batch answer requires exactly one response")
			}
		}
	} else if p.Approval == nil && p.RequestUserInput == nil && p.Interrupt == nil {
		return errors.New("resume requires an answer")
	}
	return nil
}
