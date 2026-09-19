package video_preprocess

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	"github.com/QuantumNous/new-api/common"
)

// Large video requests spill independently of the gateway-wide cache setting.
// Ordinary text/image requests never reach this path.
const videoBodyMemoryLimit = 8 << 20

// reserveStorage covers the maximum additional files of a stage, not just its
// current usage. Counting existing files as well as outstanding reservations is
// deliberately conservative: concurrent writes and abandoned workspaces cannot
// overcommit the business cap. ENOSPC from unrelated writers still propagates.
func (p *Processor) reserveStorage(cfg EngineConfig, bytes int64) (func(), error) {
	p.resourceMu.Lock()
	defer p.resourceMu.Unlock()
	if err := os.MkdirAll(cfg.Media.TempRoot, 0700); err != nil {
		return nil, wrapError(CodeStorageFailed, "temporary storage unavailable", err)
	}
	var disk syscall.Statfs_t
	if err := syscall.Statfs(cfg.Media.TempRoot, &disk); err != nil {
		return nil, wrapError(CodeStorageFailed, "temporary storage unavailable", err)
	}
	reserved := p.reservedStorageBytes + bytes
	if bytes < 0 || reserved < bytes || (cfg.Admission.TempStorageCapacity > 0 && reserved+p.actualTempBytes(cfg.Media.TempRoot) > cfg.Admission.TempStorageCapacity) {
		return nil, newError(CodeStorageExhausted, "temporary storage reservation exceeds its limit")
	}
	// Leave a small filesystem margin for metadata and concurrent system work.
	if uint64(reserved)+(16<<20) > disk.Bavail*uint64(disk.Bsize) {
		return nil, newError(CodeDiskStorageExhausted, "insufficient free temporary storage")
	}
	p.reservedStorageBytes = reserved
	return sync.OnceFunc(func() {
		p.resourceMu.Lock()
		p.reservedStorageBytes -= bytes
		p.resourceMu.Unlock()
	}), nil
}

func (p *Processor) actualTempBytes(root string) int64 {
	entries, _ := os.ReadDir(root)
	var total int64
	for _, entry := range entries {
		if entry.IsDir() && strings.HasPrefix(entry.Name(), workspacePrefix) {
			total += (&workspace{dir: filepath.Join(root, entry.Name())}).bytesOnDisk()
		}
	}
	return total
}

func (p *Processor) spoolVideoBody(ctx context.Context, cfg EngineConfig, storage common.BodyStorage) error {
	if storage.Size() < videoBodyMemoryLimit && !storage.IsDisk() {
		return nil
	}
	release, err := p.reserveStorage(cfg, storage.Size())
	if err != nil {
		return err
	}
	ws, err := newWorkspace(cfg.Media.TempRoot)
	if err != nil {
		release()
		return err
	}
	cleanup := func() { _ = ws.Cleanup(); release() }
	if err := common.SpillBodyStorage(ctx, storage, ws.dir, cleanup); err != nil {
		cleanup()
		if ctxErr := timeoutOrCancel(ctx); ctxErr != nil {
			return ctxErr
		}
		return wrapError(CodeStorageFailed, "failed to spool video request", err)
	}
	return nil
}
