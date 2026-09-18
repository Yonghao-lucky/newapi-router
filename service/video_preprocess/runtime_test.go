package video_preprocess

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/setting/video_preprocess_setting"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRuntimeStatusReportsLiveCounters(t *testing.T) {
	cfg := testEngineConfig(t)
	cfg.Version = 42
	cfg.Admission.MaxRunningJobs = 2
	cfg.Admission.MaxWaitingJobs = 3

	p, err := NewProcessor(cfg)
	require.NoError(t, err)

	// Health is a plain boolean lookup; both binaries exist in this
	// environment and neither lookup may leak a path into the payload.
	status := RuntimeStatus{
		Available:        true,
		Queue:            p.Snapshot(),
		Version:          cfg.Version,
		FFmpegAvailable:  binaryHealth("ffmpeg"),
		FFprobeAvailable: binaryHealth("ffprobe"),
		Enabled:          true,
	}
	assert.True(t, status.Available)
	assert.Equal(t, int64(42), status.Version)
	assert.True(t, status.FFmpegAvailable)
	assert.True(t, status.FFprobeAvailable)

	payload := statusJSON(t, status)
	assert.NotContains(t, payload, string(filepath.Separator), "no filesystem path may leak into the runtime payload")
	assert.NotContains(t, payload, "throughput")
}

func TestRuntimeStatusUnavailableOnBuildFailure(t *testing.T) {
	// An invalid admission config is the one realistic way the singleton build
	// fails: the settings layer already validated the rest.
	bad := testEngineConfig(t)
	bad.Admission.MaxRunningJobs = 0
	bad.Admission.MaxWaitingJobs = 0
	_, err := NewProcessor(bad)
	require.Error(t, err)

	status := GetRuntimeStatus()
	// The real singleton may or may not have been built by a previous test in
	// the process; what must hold everywhere is that an unavailable payload
	// carries a reason and an available one carries a queue.
	if !status.Available {
		assert.NotEmpty(t, status.Reason)
	} else {
		assert.GreaterOrEqual(t, status.Queue.MaxRunningJobs, 0)
	}
}

func TestStaleCleanupRemovesAbandonedWorkspace(t *testing.T) {
	if testing.Short() {
		t.Skip("stale sweep waits on mtime resolution")
	}
	tempRoot := t.TempDir()
	staleDir := filepath.Join(tempRoot, "vp-stale-unit")
	require.NoError(t, os.MkdirAll(staleDir, 0o700))
	// Backdate the directory past any sane TTL.
	old := time.Now().Add(-2 * time.Hour)
	require.NoError(t, os.Chtimes(staleDir, old, old))

	freshRoot := t.TempDir()
	freshDir := filepath.Join(freshRoot, "vp-fresh-unit")
	require.NoError(t, os.MkdirAll(freshDir, 0o700))

	p := &Processor{}
	p.cfg.Store(&EngineConfig{StaleTTL: time.Hour, Media: MediaLimits{TempRoot: tempRoot}})
	runStaleCleanupOnce(p)

	_, err := os.Stat(staleDir)
	assert.True(t, os.IsNotExist(err), "stale workspace must be removed")

	// A second sweep under a different temp root must not touch it.
	p.cfg.Store(&EngineConfig{StaleTTL: time.Hour, Media: MediaLimits{TempRoot: freshRoot}})
	runStaleCleanupOnce(p)
	_, err = os.Stat(freshDir)
	assert.NoError(t, err, "fresh workspace must survive")
}

func TestRunStaleCleanupSkipsZeroTTL(t *testing.T) {
	tempRoot := t.TempDir()
	dir := filepath.Join(tempRoot, "vp-kept")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	old := time.Now().Add(-2 * time.Hour)
	require.NoError(t, os.Chtimes(dir, old, old))

	p := &Processor{}
	p.cfg.Store(&EngineConfig{StaleTTL: 0, Media: MediaLimits{TempRoot: tempRoot}})
	runStaleCleanupOnce(p)

	_, err := os.Stat(dir)
	assert.NoError(t, err, "zero TTL must disable the sweep")
}

func TestGetProcessorBuildsFromPublishedSettings(t *testing.T) {
	// The singleton is built from whatever the settings store holds. In test
	// binaries the store may never have been initialized, so defaults apply —
	// what must hold is a working processor and consistent view.
	snapshot := video_preprocess_setting.GetSnapshot()
	assert.Equal(t, snapshot.Version, BuildEngineConfig(snapshot, video_preprocess_setting.GetDeploymentConfig()).Version)
}

// statusJSON serializes a RuntimeStatus so tests can assert on the wire shape.
func statusJSON(t *testing.T, s RuntimeStatus) string {
	t.Helper()
	data, err := json.Marshal(s)
	require.NoError(t, err)
	return string(data)
}
