package video_preprocess

import (
	"context"
	"encoding/base64"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseVideoDataURL(t *testing.T) {
	tests := []struct {
		name        string
		url         string
		allowedMIME []string
		wantCode    string
		wantSize    bool
	}{
		{
			name:     "valid mp4 data url",
			url:      "data:video/mp4;base64,AAAAIGZ0eXBpc29t",
			wantSize: true,
		},
		{
			name:     "valid webm data url",
			url:      "data:video/webm;base64,GkXfo59ChoEBQveBAULygQRC",
			wantSize: true,
		},
		{
			name:     "empty url",
			url:      "",
			wantCode: CodeInvalidDataURL,
		},
		{
			name:     "http url rejected",
			url:      "http://example.com/video.mp4",
			wantCode: CodeInvalidDataURL,
		},
		{
			name:     "https url rejected",
			url:      "https://example.com/video.mp4",
			wantCode: CodeInvalidDataURL,
		},
		{
			name:     "missing data scheme",
			url:      "video/mp4;base64,AAAA",
			wantCode: CodeInvalidDataURL,
		},
		{
			name:     "missing payload separator",
			url:      "data:video/mp4;base64",
			wantCode: CodeInvalidDataURL,
		},
		{
			name:     "empty payload",
			url:      "data:video/mp4;base64,",
			wantCode: CodeInvalidDataURL,
		},
		{
			name:     "missing base64 marker",
			url:      "data:video/mp4,AAAA",
			wantCode: CodeInvalidDataURL,
		},
		{
			name:     "non-video mime type",
			url:      "data:image/png;base64,iVBORw0KGgo=",
			wantCode: CodeUnsupportedFormat,
		},
		{
			name:        "mime not in allowlist",
			url:         "data:video/mp4;base64,AAAA",
			allowedMIME: []string{"video/webm"},
			wantCode:    CodeUnsupportedFormat,
		},
		{
			name:     "invalid base64 length",
			url:      "data:video/mp4;base64,A",
			wantCode: CodeInvalidBase64,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			u, size, err := parseVideoDataURL(tt.url, tt.allowedMIME)
			if tt.wantCode != "" {
				if err == nil {
					t.Fatalf("expected error code %s, got success", tt.wantCode)
				}
				if got := CodeOf(err); got != tt.wantCode {
					t.Errorf("got error code %q, want %q", got, tt.wantCode)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.wantSize && size <= 0 {
				t.Errorf("got size %d, want > 0", size)
			}
			if u.mime == "" {
				t.Error("parsed MIME is empty")
			}
		})
	}
}

func TestDecodeVideoDataURL(t *testing.T) {
	// Small valid base64 video header
	validPayload := "AAAAIGZ0eXBpc29tAAACAGlzb21pc28yYXZjMW1wNDE="
	dataURL := videoDataURL{
		mime:    "video/mp4",
		payload: validPayload,
		padded:  true,
	}

	t.Run("decode within limit", func(t *testing.T) {
		var buf strings.Builder
		written, err := decodeVideoDataURL(context.Background(), dataURL, &buf, 1024)
		if err != nil {
			t.Fatalf("decode failed: %v", err)
		}
		if written <= 0 {
			t.Errorf("written = %d, want > 0", written)
		}
		if buf.Len() != int(written) {
			t.Errorf("buffer size %d != written %d", buf.Len(), written)
		}
	})

	t.Run("reject oversized payload", func(t *testing.T) {
		var buf strings.Builder
		_, err := decodeVideoDataURL(context.Background(), dataURL, &buf, 10)
		if CodeOf(err) != CodeTooLarge {
			t.Errorf("got error %v, want CodeTooLarge", err)
		}
	})
}

func TestGenerateUniformTimestamps(t *testing.T) {
	tests := []struct {
		name       string
		duration   float64
		maxFrames  int
		minFrames  int
		wantMin    int
		wantMax    int
		wantAscend bool
	}{
		{
			name:       "short video 5s",
			duration:   5.0,
			maxFrames:  60,
			minFrames:  4,
			wantMin:    4,
			wantMax:    10,
			wantAscend: true,
		},
		{
			name:       "medium video 2min",
			duration:   120.0,
			maxFrames:  60,
			minFrames:  4,
			wantMin:    4,
			wantMax:    30,
			wantAscend: true,
		},
		{
			name:       "constrained by max budget",
			duration:   600.0,
			maxFrames:  10,
			minFrames:  4,
			wantMin:    10,
			wantMax:    10,
			wantAscend: true,
		},
		{
			name:       "respect minimum",
			duration:   2.0,
			maxFrames:  60,
			minFrames:  8,
			wantMin:    8,
			wantMax:    8,
			wantAscend: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ts, err := generateUniformTimestamps(tt.duration, tt.maxFrames, tt.minFrames)
			if err != nil {
				t.Fatalf("generateUniformTimestamps failed: %v", err)
			}
			if len(ts) < tt.wantMin || len(ts) > tt.wantMax {
				t.Errorf("got %d timestamps, want between %d and %d", len(ts), tt.wantMin, tt.wantMax)
			}
			if tt.wantAscend {
				for i := 1; i < len(ts); i++ {
					if ts[i] <= ts[i-1] {
						t.Errorf("timestamps not ascending: [%d]=%.3f, [%d]=%.3f", i-1, ts[i-1], i, ts[i])
					}
				}
			}
			for i, ts := range ts {
				if ts < 0 || ts >= tt.duration {
					t.Errorf("timestamp[%d]=%.3f out of range [0, %.3f)", i, ts, tt.duration)
				}
			}
		})
	}
}

func TestWorkspaceCleanup(t *testing.T) {
	tempRoot := t.TempDir()
	ws, err := newWorkspace(tempRoot)
	if err != nil {
		t.Fatalf("newWorkspace failed: %v", err)
	}

	// Create a file
	f, err := ws.create("test.bin")
	if err != nil {
		t.Fatalf("workspace.create failed: %v", err)
	}
	_, _ = f.WriteString("test data")
	_ = f.Close()

	// Verify file exists
	if _, err := os.Stat(ws.path("test.bin")); err != nil {
		t.Fatalf("created file not found: %v", err)
	}

	// Cleanup
	if err := ws.Cleanup(); err != nil {
		t.Fatalf("Cleanup failed: %v", err)
	}

	// Verify workspace directory removed
	if _, err := os.Stat(ws.dir); !os.IsNotExist(err) {
		t.Errorf("workspace directory still exists after cleanup")
	}

	// Second cleanup should be idempotent
	if err := ws.Cleanup(); err != nil {
		t.Errorf("second Cleanup failed: %v", err)
	}
}

func TestCleanupStale(t *testing.T) {
	tempRoot := t.TempDir()

	// Create fresh and stale workspaces
	ws1, _ := newWorkspace(tempRoot)
	ws2, _ := newWorkspace(tempRoot)
	defer ws1.Cleanup()
	defer ws2.Cleanup()

	// Simulate a crashed owner; a live workspace remains locked.
	_ = ws1.lock.Close()
	// Age one workspace
	oldTime := time.Now().Add(-2 * time.Hour)
	_ = os.Chtimes(ws1.dir, oldTime, oldTime)

	// Run cleanup with 1h TTL
	err := CleanupStale(tempRoot, 1*time.Hour)
	if err != nil {
		t.Fatalf("CleanupStale failed: %v", err)
	}

	// Verify stale removed, fresh kept
	if _, err := os.Stat(ws1.dir); !os.IsNotExist(err) {
		t.Errorf("stale workspace was not removed")
	}
	if _, err := os.Stat(ws2.dir); err != nil {
		t.Errorf("fresh workspace was incorrectly removed")
	}
}

func TestProcessVideoInvalidConfig(t *testing.T) {
	ctx := context.Background()
	input := ProcessVideoInput{
		Video: VideoInput{
			URL:   "data:video/mp4;base64,AAAA",
			Index: 0,
		},
		MaxFramesForThisVideo: 10,
	}

	opts := Options{
		Limits: MediaLimits{
			MaxFramesPerRequest: -1, // invalid
		},
	}

	_, err := ProcessVideo(ctx, input, opts)
	if CodeOf(err) != CodeInvalidConfig {
		t.Errorf("expected CodeInvalidConfig, got %v", err)
	}
}

func TestWorkspaceNameValidation(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{"vp-abc123", true},
		{"vp-9876543210", true},
		{"vp-", false},
		{"notprefix-123", false},
		{"vp-ABC", false},      // uppercase not allowed
		{"vp-test_123", false}, // underscore not allowed
		{"", false},
	}

	for _, tt := range tests {
		got := isWorkspaceName(tt.name)
		if got != tt.want {
			t.Errorf("isWorkspaceName(%q) = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestSniffContainer(t *testing.T) {
	tests := []struct {
		name  string
		magic []byte
		want  string
	}{
		{
			name:  "mp4 ftyp",
			magic: []byte{0, 0, 0, 0x20, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm'},
			want:  "mov",
		},
		{
			name:  "webm matroska",
			magic: []byte{0x1a, 0x45, 0xdf, 0xa3, 0, 0, 0, 0},
			want:  "matroska",
		},
		{
			name:  "unknown format",
			magic: []byte{0xff, 0xff, 0xff, 0xff},
			want:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sniffContainer(tt.magic)
			if got != tt.want {
				t.Errorf("sniffContainer() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestLimitsValidation(t *testing.T) {
	t.Run("valid defaults", func(t *testing.T) {
		limits := MediaLimits{}.withDefaults()
		if err := limits.validate(); err != nil {
			t.Errorf("default limits failed validation: %v", err)
		}
	})

	t.Run("negative max frames", func(t *testing.T) {
		limits := MediaLimits{MaxFramesPerRequest: -1}.withDefaults()
		err := limits.validate()
		if CodeOf(err) != CodeInvalidConfig {
			t.Errorf("expected CodeInvalidConfig, got %v", err)
		}
	})

	t.Run("quality out of range", func(t *testing.T) {
		limits := MediaLimits{JPEGQuality: 100}.withDefaults()
		err := limits.validate()
		if CodeOf(err) != CodeInvalidConfig {
			t.Errorf("expected CodeInvalidConfig for quality>95, got %v", err)
		}
	})

	t.Run("filesystem root rejected", func(t *testing.T) {
		limits := MediaLimits{TempRoot: "/"}.withDefaults()
		err := limits.validate()
		if CodeOf(err) != CodeInvalidConfig {
			t.Errorf("expected CodeInvalidConfig for root tempdir, got %v", err)
		}
	})
}

// Integration test requires ffmpeg/ffprobe
func TestProcessVideoIntegration(t *testing.T) {
	// Check if ffmpeg/ffprobe are available
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not found, skipping integration test")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe not found, skipping integration test")
	}

	// Create a minimal 1-second black video using ffmpeg
	tempDir := t.TempDir()
	videoPath := filepath.Join(tempDir, "test.mp4")

	cmd := exec.Command("ffmpeg",
		"-f", "lavfi",
		"-i", "color=black:s=320x240:d=1",
		"-c:v", "libx264",
		"-pix_fmt", "yuv420p",
		"-y",
		videoPath,
	)
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to generate test video: %v", err)
	}

	// Read and encode as data URL
	videoBytes, err := os.ReadFile(videoPath)
	if err != nil {
		t.Fatalf("failed to read test video: %v", err)
	}
	dataURL := "data:video/mp4;base64," + base64.StdEncoding.EncodeToString(videoBytes)

	// Process the video
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	input := ProcessVideoInput{
		Video: VideoInput{
			URL:   dataURL,
			Index: 0,
		},
		MaxFramesForThisVideo: 5,
	}

	opts := Options{
		Limits: MediaLimits{
			TempRoot:            tempDir,
			MaxFramesPerRequest: 10,
			MinFramesPerVideo:   1,
		},
	}

	result, err := ProcessVideo(ctx, input, opts)
	if err != nil {
		t.Fatalf("ProcessVideo failed: %v", err)
	}
	defer result.Cleanup()

	// Verify result
	if result.VideoIndex != 0 {
		t.Errorf("VideoIndex = %d, want 0", result.VideoIndex)
	}
	if len(result.Frames) == 0 {
		t.Error("no frames exported")
	}
	if result.Metadata.Width != 320 || result.Metadata.Height != 240 {
		t.Errorf("got dimensions %dx%d, want 320x240",
			result.Metadata.Width, result.Metadata.Height)
	}
	if result.Metadata.Container == "" {
		t.Error("container not detected")
	}

	// Verify frames exist on disk
	for i, frame := range result.Frames {
		if _, err := os.Stat(frame.Path); err != nil {
			t.Errorf("frame %d at %s does not exist: %v", i, frame.Path, err)
		}
		if frame.Bytes <= 0 {
			t.Errorf("frame %d has invalid size %d", i, frame.Bytes)
		}
	}

	// Verify cleanup removes files
	if err := result.Cleanup(); err != nil {
		t.Errorf("Cleanup failed: %v", err)
	}
	if _, err := os.Stat(result.workspace.dir); !os.IsNotExist(err) {
		t.Error("workspace not removed after cleanup")
	}
}
