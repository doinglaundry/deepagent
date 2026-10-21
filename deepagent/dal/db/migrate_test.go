package db

import (
	"context"
	"os"
	"sync"
	"testing"
)

// 专用空库：验证不同进程各自持有连接时，同时首次建表不会互相冲突。
func TestMigrateMailboxConcurrentFirstStart(t *testing.T) {
	dsn := os.Getenv("DEEPAGENT_TEST_MIGRATION_DSN")
	if dsn == "" {
		t.Skip("set DEEPAGENT_TEST_MIGRATION_DSN to a dedicated empty database")
	}
	ctx := context.Background()
	start := make(chan struct{})
	failures := make(chan error, 8)
	var workers sync.WaitGroup
	for range 8 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			client, err := NewSQL(ctx, dsn, "")
			if err != nil {
				failures <- err
				return
			}
			connection, err := client.DB(ctx, true).DB()
			if err != nil {
				failures <- err
				return
			}
			defer connection.Close()
			<-start
			failures <- MigrateMailbox(ctx, client.DB(ctx, true))
		}()
	}
	close(start)
	workers.Wait()
	close(failures)
	for err := range failures {
		if err != nil {
			t.Error(err)
		}
	}
}
