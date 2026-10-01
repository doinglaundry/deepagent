package manager

import (
	"context"

	"eino-cli/deepagent/dal/cache"
	"eino-cli/deepagent/dal/db"
	"eino-cli/deepagent/dal/model"
)

func (c *Manager) DB() *db.MySQLClient      { return c.db }
func (c *Manager) Redis() cache.RedisClient { return c.redis }

// Client is the boundary used by distributed workers. Manager satisfies it.
type Client interface {
	Acquire(context.Context, AcquireRequest) (AcquireResult, error)
	Renew(context.Context, int64, string, int64) (*Lease, error)
	ReleaseThread(context.Context, int64, string) (*model.Thread, error)
	AckInput(context.Context, int64, string, string, []int64) ([]*model.Message, error)
	ConfirmThreadClosed(context.Context, int64, string, int64) (*ThreadMessageResult, error)
	SaveOutput(context.Context, int64, string, string, []OutputFrame) error
}
