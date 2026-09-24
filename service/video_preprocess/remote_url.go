package video_preprocess

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/setting/video_preprocess_setting"
)

// RemoteURLConfig is a request snapshot. Admission and temporary-file ownership
// belong to the caller; this downloader never changes global fetch settings.
type RemoteURLConfig struct {
	Enabled          bool
	AllowlistEnabled bool
	AllowedHosts     []string
	AllowUnknownSize bool
	DownloadTimeout  time.Duration
	MaxDownloads     int

	// Used only by package tests; configuration decoding cannot populate it.
	downloader *remoteVideoDownloader
}

// RemoteDownloadResult deliberately excludes the URL, path and signed query.
type RemoteDownloadResult struct {
	SourceHost   string
	Bytes        int64
	DurationMS   int64
	FailureStage string
}

type remoteVideoResolver interface {
	LookupIPAddr(context.Context, string) ([]net.IPAddr, error)
}

// Dependencies are instance-local so tests can exercise the real TLS/HTTP
// transport without relaxing production DNS checks or certificate validation.
type remoteVideoDownloader struct {
	resolver    remoteVideoResolver
	dialContext func(context.Context, string, string) (net.Conn, error)
	roots       *x509.CertPool
}

func downloadRemoteVideo(ctx context.Context, rawURL string, dst io.Writer, maxBytes int64, cfg RemoteURLConfig) (RemoteDownloadResult, error) {
	if cfg.downloader != nil {
		return cfg.downloader.download(ctx, rawURL, dst, maxBytes, cfg)
	}
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	downloader := remoteVideoDownloader{resolver: net.DefaultResolver, dialContext: dialer.DialContext}
	return downloader.download(ctx, rawURL, dst, maxBytes, cfg)
}

func (d remoteVideoDownloader) download(ctx context.Context, rawURL string, dst io.Writer, maxBytes int64, cfg RemoteURLConfig) (result RemoteDownloadResult, err error) {
	started := time.Now()
	defer func() { result.DurationMS = time.Since(started).Milliseconds() }()
	result.FailureStage = "url"
	if !cfg.Enabled {
		return result, newError(CodeInvalidDataURL, "remote video URLs are disabled")
	}
	if cfg.DownloadTimeout <= 0 || maxBytes <= 0 || dst == nil {
		return result, newError(CodeInvalidConfig, "remote video download limits are invalid")
	}
	ctx, cancel := context.WithTimeout(ctx, cfg.DownloadTimeout)
	defer cancel()
	parsed, parseErr := url.Parse(rawURL)
	if parseErr != nil || strings.Contains(rawURL, "#") {
		return result, newError(CodeInvalidDataURL, "remote video URL is invalid")
	}
	if host, hostErr := video_preprocess_setting.NormalizeRemoteHost(parsed.Hostname()); hostErr == nil {
		result.SourceHost = host
	}
	if err = validateRemoteVideoURL(parsed, cfg); err != nil {
		return result, err
	}
	result.SourceHost = parsed.Hostname()
	transport := &http.Transport{
		Proxy: nil,
		// net/http detaches dial contexts to reuse connections. These downloads
		// have no reuse: retain the single request deadline even during DNS/dial.
		DialContext: func(_ context.Context, network, address string) (net.Conn, error) {
			return d.dialPublicAddress(ctx, network, address)
		},
		TLSClientConfig:        &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: d.roots},
		TLSHandshakeTimeout:    10 * time.Second,
		ResponseHeaderTimeout:  min(cfg.DownloadTimeout, 15*time.Second),
		MaxResponseHeaderBytes: 64 << 10,
		DisableCompression:     true,
		// Every request/redirect must resolve and validate all addresses again.
		DisableKeepAlives: true,
		TLSNextProto:      make(map[string]func(string, *tls.Conn) http.RoundTripper),
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{
		Transport: remoteVideoTransport{transport: transport, cfg: cfg},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > 3 {
				return newError(CodeInvalidDataURL, "remote video exceeded the redirect limit")
			}
			if req.Response != nil && strings.Contains(req.Response.Header.Get("Location"), "#") {
				return newError(CodeInvalidDataURL, "remote video redirect contains a fragment")
			}
			return validateRemoteVideoURL(req.URL, cfg)
		},
	}

	result.FailureStage = "head"
	response, requestErr := remoteVideoRequest(ctx, client, http.MethodHead, parsed, false)
	if requestErr != nil {
		return result, requestErr
	}
	response.Body.Close()
	expected := int64(-1)
	switch {
	case response.StatusCode >= 200 && response.StatusCode < 300:
		if err = validateRemoteVideoEncoding(response); err != nil {
			return result, err
		}
		expected = response.ContentLength
	case response.StatusCode == http.StatusMethodNotAllowed || response.StatusCode == http.StatusNotImplemented || response.StatusCode == http.StatusForbidden:
		// Some signed object-store GET URLs do not authorize HEAD.
	default:
		return result, remoteVideoStatusError("HEAD", response.StatusCode)
	}
	if expected > maxBytes {
		return result, newError(CodeTooLarge, "remote video exceeds the configured byte limit")
	}

	var download *http.Response
	if expected < 0 {
		result.FailureStage = "range"
		response, requestErr = remoteVideoRequest(ctx, client, http.MethodGet, parsed, true)
		if requestErr != nil {
			return result, requestErr
		}
		if response.StatusCode == http.StatusOK || response.StatusCode == http.StatusPartialContent {
			err = validateRemoteVideoEncoding(response)
		}
		if err != nil {
			response.Body.Close()
			return result, err
		}
		switch response.StatusCode {
		case http.StatusOK:
			// Reuse a server's ignored-Range response, never fetch it twice.
			download = response
			expected = response.ContentLength
		case http.StatusPartialContent:
			expected, err = remoteVideoRangeSize(response.Header.Get("Content-Range"))
			if err == nil && expected <= maxBytes {
				var probe [2]byte
				n, readErr := io.ReadFull(response.Body, probe[:])
				if contextErr := timeoutOrCancel(ctx); contextErr != nil {
					err = contextErr
				} else if n != 1 || (readErr != io.ErrUnexpectedEOF && readErr != io.EOF) {
					err = newError(CodeDecodeFailed, "remote video range response has an invalid length")
				}
			}
			response.Body.Close()
			if err != nil {
				return result, err
			}
		case http.StatusRequestedRangeNotSatisfiable:
			response.Body.Close()
			expected = -1
			if value := response.Header.Get("Content-Range"); value != "" {
				total, valid := strings.CutPrefix(value, "bytes */")
				size, parseErr := strconv.ParseInt(total, 10, 64)
				if !valid || parseErr != nil || size < 0 || strings.Trim(total, "0123456789") != "" {
					return result, newError(CodeDecodeFailed, "remote video range response is invalid")
				}
				expected = size
			}
		case http.StatusBadRequest, http.StatusForbidden, http.StatusMethodNotAllowed, http.StatusNotImplemented:
			response.Body.Close()
			// Some signed GET URLs reject Range. Only the existing unknown-size
			// opt-in permits one ordinary GET, still subject to all safety caps.
			expected = -1
		default:
			response.Body.Close()
			return result, remoteVideoStatusError("Range", response.StatusCode)
		}
	}
	if download != nil {
		defer download.Body.Close()
	}
	if expected > maxBytes {
		return result, newError(CodeTooLarge, "remote video exceeds the configured byte limit")
	}
	if expected < 0 && !cfg.AllowUnknownSize {
		return result, newError(CodeInvalidDataURL, "remote video size is unknown and unknown sizes are disabled")
	}
	result.FailureStage = "download"
	if download == nil {
		download, requestErr = remoteVideoRequest(ctx, client, http.MethodGet, parsed, false)
		if requestErr != nil {
			return result, requestErr
		}
		defer download.Body.Close()
	}
	if download.StatusCode != http.StatusOK {
		return result, remoteVideoStatusError("GET", download.StatusCode)
	}
	if err = validateRemoteVideoEncoding(download); err != nil {
		return result, err
	}
	if download.ContentLength > maxBytes {
		return result, newError(CodeTooLarge, "remote video exceeds the configured byte limit")
	}
	if expected >= 0 && download.ContentLength >= 0 && expected != download.ContentLength {
		return result, newError(CodeDecodeFailed, "remote video size changed between probe and download")
	}
	if expected < 0 {
		expected = download.ContentLength
	}
	var buffer [32 << 10]byte
	for {
		if err = timeoutOrCancel(ctx); err != nil {
			return result, err
		}
		remaining := maxBytes - result.Bytes
		readSize := int64(len(buffer))
		if remaining < readSize {
			readSize = remaining + 1
		}
		n, readErr := download.Body.Read(buffer[:readSize])
		result.Bytes += int64(n)
		if int64(n) > remaining {
			return result, newError(CodeTooLarge, "remote video exceeds the configured byte limit")
		}
		if n > 0 {
			written, writeErr := dst.Write(buffer[:n])
			if writeErr != nil || written != n {
				return result, newError(CodeStorageFailed, "remote video could not be stored")
			}
		}
		if readErr == nil {
			continue
		}
		if err = timeoutOrCancel(ctx); err != nil {
			return result, err
		}
		if readErr != io.EOF {
			return result, newError(CodeDecodeFailed, "remote video download ended before completion")
		}
		break
	}
	if result.Bytes == 0 || (expected >= 0 && result.Bytes != expected) {
		return result, newError(CodeDecodeFailed, "remote video download length does not match the expected size")
	}
	result.FailureStage = ""
	return result, nil
}

type remoteVideoTransport struct {
	transport *http.Transport
	cfg       RemoteURLConfig
}

func (t remoteVideoTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := validateRemoteVideoURL(req.URL, t.cfg); err != nil {
		return nil, err
	}
	// Go adds Referer automatically on redirects. Signed source URLs must not
	// leak to the redirected origin, even if both origins are allowlisted.
	req.Header.Del("Referer")
	req.Header.Del("Authorization")
	req.Header.Del("Proxy-Authorization")
	req.Header.Del("Cookie")
	return t.transport.RoundTrip(req)
}

func validateRemoteVideoURL(target *url.URL, cfg RemoteURLConfig) error {
	if target == nil || target.Scheme != "https" || target.Opaque != "" || target.User != nil || target.Fragment != "" || target.RawFragment != "" {
		return newError(CodeInvalidDataURL, "remote video requires an HTTPS URL without credentials or a fragment")
	}
	if target.Port() != "" && target.Port() != "443" {
		return newError(CodeInvalidDataURL, "remote video URLs must use port 443")
	}
	host, err := video_preprocess_setting.NormalizeRemoteHost(target.Hostname())
	if err != nil || strings.HasSuffix(target.Host, ":") {
		return newError(CodeInvalidDataURL, "remote video URL hostname is not allowed")
	}
	if cfg.AllowlistEnabled {
		allowed := false
		for _, configured := range cfg.AllowedHosts {
			candidate, candidateErr := video_preprocess_setting.NormalizeRemoteHost(configured)
			if candidateErr == nil && candidate == host {
				allowed = true
				break
			}
		}
		if !allowed {
			return newError(CodeInvalidDataURL, "remote video hostname is not in the allowed list")
		}
	}
	if target.Port() == "443" {
		target.Host = net.JoinHostPort(host, "443")
	} else {
		target.Host = host
	}
	return nil
}

func (d remoteVideoDownloader) dialPublicAddress(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil || port != "443" {
		return nil, newError(CodeInvalidDataURL, "remote video connection target is invalid")
	}
	addresses, err := d.resolver.LookupIPAddr(ctx, host)
	if err != nil || len(addresses) == 0 {
		return nil, newError(CodeDecodeFailed, "remote video hostname could not be resolved")
	}
	validated := make([]netip.Addr, 0, len(addresses))
	for _, address := range addresses {
		ip, ok := netip.AddrFromSlice(address.IP)
		if !ok || address.Zone != "" || !isPublicRemoteVideoIP(ip) {
			return nil, newError(CodeInvalidDataURL, "remote video hostname resolves to a non-public address")
		}
		validated = append(validated, ip.Unmap())
	}
	// Dial IP literals from this single validated resolution. The TLS transport
	// retains the request hostname as ServerName, including on redirects.
	for _, ip := range validated {
		conn, dialErr := d.dialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		if dialErr == nil {
			return conn, nil
		}
		if err := timeoutOrCancel(ctx); err != nil {
			return nil, err
		}
	}
	return nil, newError(CodeDecodeFailed, "remote video server could not be reached")
}

var remoteVideoBlockedNetworks = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"), netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("169.254.0.0/16"), netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("168.63.129.16/32"), // Azure platform virtual address.
	netip.MustParsePrefix("192.0.0.0/24"), netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"), netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("198.18.0.0/15"), netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"), netip.MustParsePrefix("224.0.0.0/4"),
	netip.MustParsePrefix("240.0.0.0/4"),
	// Special-purpose IPv6, including Teredo, benchmarking and ORCHID.
	netip.MustParsePrefix("2001::/23"), netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2002::/16"), netip.MustParsePrefix("3fff::/20"),
}

func isPublicRemoteVideoIP(ip netip.Addr) bool {
	if !ip.IsValid() || ip.Zone() != "" {
		return false
	}
	ip = ip.Unmap()
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return false
	}
	// Only the currently allocated global-unicast block is eligible. This also
	// excludes NAT64, IPv4-compatible, ULA and unallocated transition ranges.
	if ip.Is6() && !netip.MustParsePrefix("2000::/3").Contains(ip) {
		return false
	}
	for _, blocked := range remoteVideoBlockedNetworks {
		if blocked.Contains(ip) {
			return false
		}
	}
	return true
}

func remoteVideoRequest(ctx context.Context, client *http.Client, method string, target *url.URL, rangeProbe bool) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, target.String(), nil)
	if err != nil {
		return nil, newError(CodeInvalidDataURL, "remote video request could not be created")
	}
	req.Header.Set("Accept-Encoding", "identity")
	if rangeProbe {
		req.Header.Set("Range", "bytes=0-0")
	}
	response, err := client.Do(req)
	if err != nil {
		if response != nil && response.Body != nil {
			response.Body.Close()
		}
		if contextErr := timeoutOrCancel(ctx); contextErr != nil {
			return nil, contextErr
		}
		var timeoutErr net.Error
		if errors.As(err, &timeoutErr) && timeoutErr.Timeout() {
			return nil, newError(CodeProcessingTimeout, "remote video HTTP request exceeded its time budget")
		}
		var safeErr *Error
		if errors.As(err, &safeErr) {
			return nil, safeErr
		}
		// url.Error and net errors can include a signed URL or private address.
		return nil, newError(CodeDecodeFailed, "remote video HTTP request failed")
	}
	return response, nil
}

func validateRemoteVideoEncoding(response *http.Response) error {
	if encoding := strings.Join(response.Header.Values("Content-Encoding"), ","); encoding != "" && !strings.EqualFold(strings.TrimSpace(encoding), "identity") {
		return newError(CodeDecodeFailed, "remote video content encoding is not supported")
	}
	return nil
}

// These are source-file HTTP failures, not model-provider statuses. Keep the
// existing preprocessing code/status and never include the source URL/body.
func remoteVideoStatusError(stage string, status int) *Error {
	if stage != "HEAD" && stage != "Range" && stage != "GET" {
		stage = "HTTP"
	}
	message := "remote video source rejected " + stage + " request (HTTP " + strconv.Itoa(status) + ")"
	err := newError(CodeDecodeFailed, message)
	err.publicMessage = message
	return err
}

func remoteVideoRangeSize(value string) (int64, error) {
	total, ok := strings.CutPrefix(value, "bytes 0-0/")
	if !ok {
		return 0, newError(CodeDecodeFailed, "remote video range response is invalid")
	}
	if total == "*" {
		return -1, nil
	}
	size, err := strconv.ParseInt(total, 10, 64)
	if err != nil || size <= 0 {
		return 0, newError(CodeDecodeFailed, "remote video range size is invalid")
	}
	return size, nil
}
