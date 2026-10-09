//go:build !windows

package worker

import (
	"time"

	dalmodel "eino-cli/deepagent/dal/model"
)

const (
	defaultConcurrency             = 1
	defaultLeaseMS                 = int64(60_000)
	defaultScanInterval            = time.Second
	defaultMessagePollInterval     = 500 * time.Millisecond
	defaultIdleTimeout             = 10 * time.Second
	defaultShutdownDrainTimeout    = 120 * time.Second
	defaultShutdownInterruptDrain  = 120 * time.Second
	defaultInterruptDrainTimeout   = 30 * time.Second
	defaultRuntimeInterruptTimeout = 15 * time.Second
	defaultAppendEventAttempts     = 3
	defaultAppendEventRetryDelay   = 100 * time.Millisecond
	defaultReleaseReason           = "agent thread completed"
	defaultErrorReleaseReason      = "agent thread failed"
	postMessageFailedReason        = "agent thread post message failed"
	ackMessageFailedReason         = "agent thread ack failed"
	controlInputFailedReason       = "agent thread control input failed"
	interruptFailedReason          = "agent thread interrupt failed"
	defaultGracefulReleaseReason   = "worker graceful exit"
	defaultShutdownTimeoutReason   = "worker graceful exit timeout"
	defaultInterruptTimeoutReason  = "agent thread interrupt timeout"
	defaultThreadClosedReason      = "agent thread closed"
	defaultCloseThreadReason       = "user_close"
	maxPullErrorBackoff            = 5 * time.Second
)

const (
	// MessageTypeControlCancelInput is the Manager mailbox message type
	// used by control-plane callers to cancel input up to a cutoff message.
	MessageTypeControlCancelInput = dalmodel.ControlMessageTypeCancelInput
	// MessageTypeControlCloseThread is the Manager mailbox message type
	// used by control-plane callers to close a thread.
	MessageTypeControlCloseThread = dalmodel.ControlMessageTypeCloseThread
)
