package db

import "eino-cli/deepagent/dal/model"

type Message = model.Message
type Sender = model.Sender
type MessageFilter = model.MessageFilter

const (
	MessageTypeInput      = "input"
	MessageTypeControl    = "control"
	MessageTypeOutput     = "output"
	MessageStatusPending  = model.MessageStatusPending
	MessageStatusAccepted = model.MessageStatusAcked
	MessageStatusFinished = model.MessageStatusCompleted
	MessageStatusCanceled = model.MessageStatusCanceled
)
