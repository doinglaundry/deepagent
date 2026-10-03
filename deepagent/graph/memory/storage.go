package memory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	memorypkg "eino-cli/deepagent/protocol/memory"

	"github.com/google/uuid"
)

func (memoryService *memoryService) buildMemoryKey(scope, suffix string) string {
	return scope + "/" + suffix
}

// Independent renewable leases protect long-term jobs; a lost lease cancels model work
// and CompleteMemory fences every artifact/baseline mutation against the live token.
func (memoryService *memoryService) runLeasedJob(ctx context.Context, key string, work func(context.Context, memorypkg.Lease) error) error {
	lease, operationErr := memoryService.c.Store.ClaimMemory(ctx, key, uuid.NewString(), memoryService.c.LeaseTTL)
	if operationErr != nil {
		return operationErr
	}
	ctx, cancel := context.WithCancelCause(ctx)
	done := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		ticker := time.NewTicker(memoryService.c.LeaseTTL / 3)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				_, operationErr := memoryService.c.Store.RenewMemory(ctx, lease, memoryService.c.LeaseTTL)
				if operationErr != nil {
					cancel(operationErr)
					return
				}
			}
		}
	}()
	defer func() {
		close(done)
		cancel(nil)
		<-stopped
		releaseCtx, cancelRelease := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancelRelease()
		_ = memoryService.c.Store.ReleaseMemory(releaseCtx, lease)
	}()
	err := work(ctx, lease)
	return errors.Join(err, context.Cause(ctx))
}

func (memoryService *memoryService) readSharedState(ctx context.Context, scope string) (consolidated, error) {
	memoryState := consolidated{Baselines: map[string]string{}}
	artifact, operationErr := memoryService.c.Store.GetMemory(ctx, memoryService.buildMemoryKey(scope, "consolidation"))
	if errors.Is(operationErr, memorypkg.ErrNotFound) {
		return memoryState, nil
	}
	if operationErr != nil {
		return memoryState, operationErr
	}
	if len(artifact.Data) > 0 {
		operationErr = json.Unmarshal(artifact.Data, &memoryState)
	}
	return memoryState, operationErr
}

func hashBytes(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func (memoryService *memoryService) buildScopeRoot(scope string) string {
	return filepath.Join(memoryService.c.Root, hashBytes([]byte(scope)))
}

func (memoryService *memoryService) readLocalState(scope string) (consolidated, error) {
	state := consolidated{Baselines: map[string]string{}}
	err := readJSON(filepath.Join(memoryService.buildScopeRoot(scope), "memory.json"), &state)
	if errors.Is(err, os.ErrNotExist) {
		err = nil
	}
	return state, err
}

func readJSON(path string, value any) error {
	encodedJSON, operationErr := os.ReadFile(path)
	if operationErr != nil {
		return operationErr
	}
	operationErr = json.Unmarshal(encodedJSON, value)
	if operationErr != nil {
		return fmt.Errorf("read %s: %w", path, operationErr)
	}
	return nil
}

func writeAtomicJSON(path string, value any) error {
	encodedJSON, operationErr := json.MarshalIndent(value, "", "  ")
	if operationErr != nil {
		return operationErr
	}
	temporaryFile, operationErr := os.CreateTemp(filepath.Dir(path), ".memory-*")
	if operationErr != nil {
		return operationErr
	}
	temporaryPath := temporaryFile.Name()
	defer os.Remove(temporaryPath)
	_, operationErr = temporaryFile.Write(encodedJSON)
	if operationErr != nil {
		temporaryFile.Close()
		return operationErr
	}
	operationErr = temporaryFile.Sync()
	if operationErr != nil {
		temporaryFile.Close()
		return operationErr
	}
	operationErr = temporaryFile.Close()
	if operationErr != nil {
		return operationErr
	}
	operationErr = os.Rename(temporaryPath, path)
	if operationErr != nil {
		return operationErr
	}
	directory, operationErr := os.Open(filepath.Dir(path))
	if operationErr != nil {
		return operationErr
	}
	defer directory.Close()
	return directory.Sync()
}

func acquireFileLock(ctx context.Context, path string) (func(), error) {
	lockFile, operationErr := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if operationErr != nil {
		return nil, operationErr
	}
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		operationErr = syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if operationErr == nil {
			return func() { _ = syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN); _ = lockFile.Close() }, nil
		}
		if !errors.Is(operationErr, syscall.EWOULDBLOCK) && !errors.Is(operationErr, syscall.EAGAIN) {
			lockFile.Close()
			return nil, operationErr
		}
		select {
		case <-ctx.Done():
			lockFile.Close()
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}
