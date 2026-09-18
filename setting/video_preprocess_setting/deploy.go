package video_preprocess_setting

import (
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/QuantumNous/new-api/common"
)

// Deployment-level keys (plan §8.3). These describe the machine the process
// runs on — binaries, mounts, container resources — so they are read from the
// environment at startup and are never writable through the settings API.
const (
	FFmpegPathEnv     = "VIDEO_PREPROCESS_FFMPEG_PATH"
	FFprobePathEnv    = "VIDEO_PREPROCESS_FFPROBE_PATH"
	TempDirEnv        = "VIDEO_PREPROCESS_TEMP_DIR"
	HWAccelEnabledEnv = "VIDEO_PREPROCESS_HWACCEL_ENABLED"
)

// DeploymentConfig is the read-only view of the start-up configuration. The
// settings API returns it so the Web page can show what the process was
// deployed with and why the values are not editable.
type DeploymentConfig struct {
	FFmpegPath     string `json:"ffmpeg_path"`
	FFprobePath    string `json:"ffprobe_path"`
	TempDir        string `json:"temp_dir"`
	HWAccelEnabled bool   `json:"hwaccel_enabled"`
}

var (
	deployOnce   sync.Once
	deployConfig DeploymentConfig
)

// defaultTempDir is where request-scoped media lands when the deployment does
// not pin a directory. A dedicated mount is preferred in production; the OS
// temp directory is the portable fallback.
func defaultTempDir() string {
	if configured := strings.TrimSpace(os.Getenv(TempDirEnv)); configured != "" {
		return configured
	}
	return filepath.Join(os.TempDir(), "new-api-video-preprocess")
}

// loadDeploymentConfig resolves the deployment values once. Paths are not
// checked for existence here: a missing binary must surface when a job needs
// it, not by refusing to start the whole gateway.
func loadDeploymentConfig() {
	deployConfig = DeploymentConfig{
		FFmpegPath:     common.GetEnvOrDefaultString(FFmpegPathEnv, "ffmpeg"),
		FFprobePath:    common.GetEnvOrDefaultString(FFprobePathEnv, "ffprobe"),
		TempDir:        defaultTempDir(),
		HWAccelEnabled: common.GetEnvOrDefaultBool(HWAccelEnabledEnv, false),
	}
}

// GetDeploymentConfig returns the deployment values. The result is a value
// copy, so callers cannot mutate what other callers observe.
func GetDeploymentConfig() DeploymentConfig {
	deployOnce.Do(loadDeploymentConfig)
	return deployConfig
}

// TempDiskInfo reports the filesystem holding the temp directory, so the Web
// runtime card can show physical free space next to the configured business
// budget. Zeroes mean the information is unavailable on this platform.
type TempDiskInfo struct {
	TotalBytes uint64 `json:"total_bytes"`
	FreeBytes  uint64 `json:"free_bytes"`
}

// GetTempDiskInfo probes the filesystem backing the configured temp directory.
// The directory is created on demand because the probe needs an existing path
// and a deployment that has not yet run a job may not have one. A failure to
// create it is reported and yields zeroes rather than an error: this feeds a
// warning and a read-only display, neither of which may break a settings read.
func GetTempDiskInfo() TempDiskInfo {
	tempDir := GetDeploymentConfig().TempDir
	if err := os.MkdirAll(tempDir, 0o700); err != nil {
		common.SysError("video preprocess: prepare temp dir for disk probe: " + err.Error())
		return TempDiskInfo{}
	}
	total, free, ok := diskSpaceForPath(tempDir)
	if !ok {
		return TempDiskInfo{}
	}
	return TempDiskInfo{TotalBytes: total, FreeBytes: free}
}
