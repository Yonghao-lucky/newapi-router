// Command video-preprocess-bench measures the actual video middleware over HTTP
// with a local upstream. It deliberately excludes authentication, database
// telemetry and model billing; router regression tests cover those contracts.
// Results describe this machine and fixture only, not production capacity.
package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/service/video_preprocess"
	"github.com/QuantumNous/new-api/setting/video_preprocess_setting"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

type latency struct {
	Count int     `json:"count"`
	P50   float64 `json:"p50_ms"`
	P95   float64 `json:"p95_ms"`
	P99   float64 `json:"p99_ms"`
}
type runResult struct {
	Configuration                                        string `json:"configuration"`
	Workload                                             string `json:"workload"`
	Enabled                                              bool   `json:"enabled"`
	Requests, Concurrency, Errors                        int
	Seconds, RequestsPerSecond                           float64
	All, Text, Image, Video                              latency
	PeakTempBytes, PeakHeapBytes                         int64
	PeakMediaProcesses, PeakRunningJobs, PeakWaitingJobs int
	TextP95DeltaMS, ImageP95DeltaMS                      float64
}
type report struct {
	Boundary, Fixture, FFmpeg, FFprobe, CPU, Memory, Sampling string
	CPUs                                                      int
	DiskTotal, DiskFree                                       uint64
	Results                                                   []runResult
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	count := flag.Int("requests", 120, "requests per workload, a positive multiple of ten")
	concurrency := flag.Int("concurrency", 48, "concurrent HTTP clients")
	fixture := flag.String("fixture", "", "optional local MP4 fixture; otherwise generate 3s 320x180 H.264")
	output := flag.String("output", "", "JSON output file; defaults to stdout")
	flag.Parse()
	if *count < 10 || *count%10 != 0 || *concurrency < 1 {
		return fmt.Errorf("requests must be a positive multiple of ten and concurrency must be positive")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	root, err := os.MkdirTemp("", "video-benchmark-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(root)
	ffmpegVersion, err := exec.CommandContext(ctx, "ffmpeg", "-version").Output()
	if err != nil {
		return err
	}
	ffprobeVersion, err := exec.CommandContext(ctx, "ffprobe", "-version").Output()
	if err != nil {
		return err
	}
	description := *fixture
	if *fixture == "" {
		*fixture = filepath.Join(root, "fixture.mp4")
		data, err := exec.CommandContext(ctx, "ffmpeg", "-nostdin", "-v", "error", "-f", "lavfi", "-i", "testsrc2=s=320x180:r=12:d=3", "-threads", "1", "-c:v", "libx264", "-preset", "fast", *fixture).CombinedOutput()
		if err != nil {
			return fmt.Errorf("fixture generation: %w: %s", err, data)
		}
		description = "generated testsrc2; H.264 MP4; 3s; 320x180; 12fps; libx264 preset fast"
	}
	video, err := os.ReadFile(*fixture)
	if err != nil {
		return err
	}
	description += fmt.Sprintf("; %d bytes", len(video))
	imageData := image.NewRGBA(image.Rect(0, 0, 160, 120))
	for y := range 120 {
		for x := range 160 {
			imageData.SetRGBA(x, y, color.RGBA{uint8(x), uint8(y), 90, 255})
		}
	}
	var jpegData bytes.Buffer
	if err := jpeg.Encode(&jpegData, imageData, &jpeg.Options{Quality: 82}); err != nil {
		return err
	}
	textBody := []byte(`{"model":"kimi-k3","messages":[{"role":"user","content":"Describe the visual content and explain the main changes."}]}`)
	imageBody := []byte(`{"model":"kimi-k3","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/jpeg;base64,` + base64.StdEncoding.EncodeToString(jpegData.Bytes()) + `"}}]}]}`)
	videoBody := []byte(`{"model":"kimi-k3","messages":[{"role":"user","content":[{"type":"video_url","video_url":{"url":"data:video/mp4;base64,` + base64.StdEncoding.EncodeToString(video) + `"}}]}]}`)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil || !gjson.ValidBytes(body) {
			w.WriteHeader(400)
			return
		}
		if r.Header.Get("X-Benchmark-Expect-Image") == "true" && (bytes.Contains(body, []byte(`"video_url"`)) || !bytes.Contains(body, []byte(`"image_url"`))) {
			w.WriteHeader(422)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"benchmark","choices":[{"message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":12,"completion_tokens":3,"total_tokens":15}}`)
	}))
	defer upstream.Close()
	upstreamURL, err := url.Parse(upstream.URL)
	if err != nil {
		return err
	}
	proxy := httputil.NewSingleHostReverseProxy(upstreamURL)
	proxy.Transport = &http.Transport{MaxIdleConnsPerHost: *concurrency, MaxIdleConns: *concurrency}
	defer proxy.Transport.(*http.Transport).CloseIdleConnections()
	gin.SetMode(gin.ReleaseMode)
	router := gin.New()
	router.Use(middleware.BodyStorageCleanup(), func(c *gin.Context) { common.SetContextKey(c, constant.ContextKeyUserId, 1) })
	router.POST("/v1/chat/completions", middleware.VideoPreprocess(), func(c *gin.Context) { proxy.ServeHTTP(c.Writer, c.Request) })
	server := httptest.NewServer(router)
	defer server.Close()
	transport := &http.Transport{MaxIdleConnsPerHost: *concurrency, MaxIdleConns: *concurrency}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 5 * time.Minute}
	processor := video_preprocess.GetProcessor()
	if processor == nil {
		return fmt.Errorf("video processor unavailable")
	}
	cpu, _ := os.ReadFile("/proc/cpuinfo")
	memory, _ := os.ReadFile("/proc/meminfo")
	cpuLine := ""
	for line := range strings.SplitSeq(string(cpu), "\n") {
		if strings.HasPrefix(line, "model name") {
			cpuLine = line
			break
		}
	}
	memoryLine, _, _ := strings.Cut(string(memory), "\n")
	var disk syscall.Statfs_t
	if err := syscall.Statfs(root, &disk); err != nil {
		return err
	}
	ffmpegLine, _, _ := strings.Cut(string(ffmpegVersion), "\n")
	ffprobeLine, _, _ := strings.Cut(string(ffprobeVersion), "\n")
	result := report{Boundary: "HTTP -> BodyStorageCleanup -> VideoPreprocess -> HTTP local upstream; excludes authentication, DB completion writes, routing and billing", Fixture: description, FFmpeg: ffmpegLine, FFprobe: ffprobeLine, CPU: cpuLine, Memory: memoryLine, CPUs: runtime.NumCPU(), DiskTotal: disk.Blocks * uint64(disk.Bsize), DiskFree: disk.Bavail * uint64(disk.Bsize), Sampling: "temp files, child processes and heap sampled every 5ms; percentiles use nearest rank; small-scale closed-loop load"}
	for _, pair := range [][2]int{{12, 4}, {16, 3}, {20, 3}, {24, 2}} {
		settings := video_preprocess_setting.DefaultSettings()
		settings.MaxRunningJobs, settings.FFmpegThreadsPerJob = pair[0], pair[1]
		settings.MaxRunningPerUser = pair[0]
		settings.MaxWaitingPerUser = 500
		settings.MaxEstimatedWaitSeconds = 3600
		settings.TelemetryEnabled = false
		cfg := video_preprocess.BuildEngineConfig(video_preprocess_setting.Snapshot{Settings: settings}, video_preprocess_setting.DeploymentConfig{FFmpegPath: "ffmpeg", FFprobePath: "ffprobe", TempDir: filepath.Join(root, "media")})
		var baseline runResult
		for _, scenario := range []struct {
			name              string
			enabled, allVideo bool
		}{{"mixed-baseline", false, false}, {"70text-20image-10video", true, false}, {"100video", true, true}} {
			cfg.Enabled = scenario.enabled
			if err := processor.SetConfig(cfg); err != nil {
				return err
			}
			current := runResult{Configuration: fmt.Sprintf("%dx%d", pair[0], pair[1]), Workload: scenario.name, Enabled: scenario.enabled, Requests: *count, Concurrency: *concurrency}
			stop := make(chan struct{})
			sampleDone := make(chan struct{})
			go func() {
				defer close(sampleDone)
				ticker := time.NewTicker(5 * time.Millisecond)
				defer ticker.Stop()
				for {
					select {
					case <-stop:
						return
					case <-ticker.C:
						snapshot := processor.Snapshot()
						current.PeakTempBytes = max(current.PeakTempBytes, snapshot.TempStorageBytes)
						current.PeakMediaProcesses = max(current.PeakMediaProcesses, snapshot.ActiveMediaProcesses)
						current.PeakRunningJobs = max(current.PeakRunningJobs, snapshot.RunningJobs)
						current.PeakWaitingJobs = max(current.PeakWaitingJobs, snapshot.WaitingJobs)
						var memory runtime.MemStats
						runtime.ReadMemStats(&memory)
						current.PeakHeapBytes = max(current.PeakHeapBytes, int64(memory.HeapAlloc))
					}
				}
			}()
			var next atomic.Int64
			durations := make([]float64, *count)
			kinds := make([]int, *count)
			failures := make([]bool, *count)
			started := time.Now()
			var workers sync.WaitGroup
			for range *concurrency {
				workers.Go(func() {
					for {
						index := int(next.Add(1)) - 1
						if index >= *count {
							return
						}
						kind := 0
						if scenario.allVideo || index%10 == 9 {
							kind = 2
						} else if index%10 >= 7 {
							kind = 1
						}
						kinds[index] = kind
						body := textBody
						if kind == 1 {
							body = imageBody
						}
						if kind == 2 {
							body = videoBody
						}
						begin := time.Now()
						request, err := http.NewRequestWithContext(ctx, "POST", server.URL+"/v1/chat/completions", bytes.NewReader(body))
						if err != nil {
							failures[index] = true
							durations[index] = time.Since(begin).Seconds() * 1000
							continue
						}
						request.Header.Set("Content-Type", "application/json")
						if kind == 2 && scenario.enabled {
							request.Header.Set("X-Benchmark-Expect-Image", "true")
						}
						response, err := client.Do(request)
						if err == nil {
							_, readErr := io.Copy(io.Discard, response.Body)
							_ = response.Body.Close()
							failures[index] = readErr != nil || response.StatusCode != 200
						} else {
							failures[index] = true
						}
						durations[index] = time.Since(begin).Seconds() * 1000
					}
				})
			}
			workers.Wait()
			current.Seconds = time.Since(started).Seconds()
			close(stop)
			<-sampleDone
			current.RequestsPerSecond = float64(*count) / current.Seconds
			current.All = percentiles(durations)
			var grouped [3][]float64
			for i, duration := range durations {
				grouped[kinds[i]] = append(grouped[kinds[i]], duration)
				if failures[i] {
					current.Errors++
				}
			}
			current.Text, current.Image, current.Video = percentiles(grouped[0]), percentiles(grouped[1]), percentiles(grouped[2])
			if !scenario.enabled {
				baseline = current
			} else if !scenario.allVideo {
				current.TextP95DeltaMS = current.Text.P95 - baseline.Text.P95
				current.ImageP95DeltaMS = current.Image.P95 - baseline.Image.P95
			}
			result.Results = append(result.Results, current)
			fmt.Fprintf(os.Stderr, "%s %s: %.2f req/s, p95 %.2fms, errors %d/%d\n", current.Configuration, current.Workload, current.RequestsPerSecond, current.All.P95, current.Errors, current.Requests)
			snapshot := processor.Snapshot()
			if snapshot.RunningJobs != 0 || snapshot.WaitingJobs != 0 || snapshot.ActiveMediaProcesses != 0 || snapshot.TempStorageBytes != 0 {
				return fmt.Errorf("resources remain after workload")
			}
		}
	}
	data, err := common.Marshal(result)
	if err != nil {
		return err
	}
	data, err = common.IndentJson(data)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if *output != "" {
		return os.WriteFile(*output, data, 0600)
	}
	_, err = os.Stdout.Write(data)
	return err
}
func percentiles(values []float64) latency {
	if len(values) == 0 {
		return latency{}
	}
	sorted := slices.Clone(values)
	slices.Sort(sorted)
	return latency{Count: len(sorted), P50: sorted[int(math.Ceil(float64(len(sorted))*.5))-1], P95: sorted[int(math.Ceil(float64(len(sorted))*.95))-1], P99: sorted[int(math.Ceil(float64(len(sorted))*.99))-1]}
}
