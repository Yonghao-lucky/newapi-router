package video_preprocess

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type remoteVideoResolverFunc func(context.Context, string) ([]net.IPAddr, error)

func (f remoteVideoResolverFunc) LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error) {
	return f(ctx, host)
}

// This fixture exercises the production HTTPS transport and real certificate
// verification. Only the final TCP destination is mapped into the test server.
func newRemoteVideoTLSFixture(t *testing.T, handler http.Handler) remoteVideoDownloader {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	certificate := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "video download fixture"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		DNSNames:    []string{"video.example.test", "redirect.example.test"},
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IsCA:        true, BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, certificate, certificate, &key.PublicKey, key)
	require.NoError(t, err)
	trusted, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	roots := x509.NewCertPool()
	roots.AddCert(trusted)
	server := httptest.NewUnstartedServer(handler)
	server.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}, MinVersion: tls.VersionTLS12}
	server.StartTLS()
	t.Cleanup(server.Close)
	dialer := &net.Dialer{Timeout: time.Second}
	return remoteVideoDownloader{
		roots: roots,
		resolver: remoteVideoResolverFunc(func(ctx context.Context, host string) ([]net.IPAddr, error) {
			return []net.IPAddr{{IP: net.ParseIP("93.184.216.34")}}, nil
		}),
		dialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			assert.Equal(t, "93.184.216.34:443", address, "the transport must pin the validated IP")
			return dialer.DialContext(ctx, network, server.Listener.Addr().String())
		},
	}
}

func TestRemoteVideoSizeProbesAndBoundedDownload(t *testing.T) {
	for _, tc := range []struct {
		name, headLength, rangeSize, body, getLength, encoding, wantCode string
		headStatus, getStatus, rangeStatus                               int
		rangeIgnored, unknownRejected, chunked, headEncoded              bool
		wantMethods                                                      []string
	}{
		{name: "known", headLength: "3", body: "abc", wantMethods: []string{"HEAD", "GET"}},
		{name: "head oversize", headLength: "5", wantCode: CodeTooLarge, wantMethods: []string{"HEAD"}},
		{name: "range size", headStatus: 405, rangeSize: "bytes 0-0/3", body: "abc", wantMethods: []string{"HEAD", "GET bytes=0-0", "GET"}},
		{name: "signed GET denies HEAD", headStatus: 403, rangeSize: "bytes 0-0/3", body: "abc", wantMethods: []string{"HEAD", "GET bytes=0-0", "GET"}},
		{name: "encoded HEAD denial is not video encoding", headStatus: 403, headEncoded: true, encoding: "gzip", rangeSize: "bytes 0-0/5", wantCode: CodeTooLarge, wantMethods: []string{"HEAD", "GET bytes=0-0"}},
		{name: "range oversize", rangeSize: "bytes 0-0/5", wantCode: CodeTooLarge, wantMethods: []string{"HEAD", "GET bytes=0-0"}},
		{name: "ignored range reuses body", rangeIgnored: true, body: "abc", wantMethods: []string{"HEAD", "GET bytes=0-0"}},
		{name: "unknown chunked", rangeIgnored: true, chunked: true, body: "abc", wantMethods: []string{"HEAD", "GET bytes=0-0"}},
		{name: "unknown bounded", rangeIgnored: true, chunked: true, body: "abcde", wantCode: CodeTooLarge, wantMethods: []string{"HEAD", "GET bytes=0-0"}},
		{name: "unknown forbidden", rangeIgnored: true, chunked: true, unknownRejected: true, body: "abc", wantCode: CodeInvalidDataURL, wantMethods: []string{"HEAD", "GET bytes=0-0"}},
		{name: "invalid range", rangeSize: "bytes 0-1/3", wantCode: CodeDecodeFailed, wantMethods: []string{"HEAD", "GET bytes=0-0"}},
		{name: "unknown range total", rangeSize: "bytes 0-0/*", body: "abc", wantMethods: []string{"HEAD", "GET bytes=0-0", "GET"}},
		{name: "range unsupported", rangeStatus: 405, body: "abc", wantMethods: []string{"HEAD", "GET bytes=0-0", "GET"}},
		{name: "range forbidden permits bounded GET", headStatus: 403, rangeStatus: 403, body: "abc", wantMethods: []string{"HEAD", "GET bytes=0-0", "GET"}},
		{name: "range bad request permits bounded GET", rangeStatus: 400, body: "abc", wantMethods: []string{"HEAD", "GET bytes=0-0", "GET"}},
		{name: "range forbidden unknown disabled", rangeStatus: 403, unknownRejected: true, wantCode: CodeInvalidDataURL, wantMethods: []string{"HEAD", "GET bytes=0-0"}},
		{name: "range fallback GET rejected", rangeStatus: 403, getStatus: 403, wantCode: CodeDecodeFailed, wantMethods: []string{"HEAD", "GET bytes=0-0", "GET"}},
		{name: "range fallback hard cap", rangeStatus: 400, chunked: true, body: "abcde", wantCode: CodeTooLarge, wantMethods: []string{"HEAD", "GET bytes=0-0", "GET"}},
		{name: "range unauthorized does not fallback", rangeStatus: 401, wantCode: CodeDecodeFailed, wantMethods: []string{"HEAD", "GET bytes=0-0"}},
		{name: "range missing does not fallback", rangeStatus: 404, wantCode: CodeDecodeFailed, wantMethods: []string{"HEAD", "GET bytes=0-0"}},
		{name: "range throttled does not fallback", rangeStatus: 429, wantCode: CodeDecodeFailed, wantMethods: []string{"HEAD", "GET bytes=0-0"}},
		{name: "range unavailable does not fallback", rangeStatus: 503, wantCode: CodeDecodeFailed, wantMethods: []string{"HEAD", "GET bytes=0-0"}},
		{name: "unsatisfied oversize", rangeStatus: 416, rangeSize: "bytes */5", wantCode: CodeTooLarge, wantMethods: []string{"HEAD", "GET bytes=0-0"}},
		{name: "unsatisfied known size", rangeStatus: 416, rangeSize: "bytes */3", unknownRejected: true, body: "abc", wantMethods: []string{"HEAD", "GET bytes=0-0", "GET"}},
		{name: "range unsupported unknown denied", rangeStatus: 501, unknownRejected: true, wantCode: CodeInvalidDataURL, wantMethods: []string{"HEAD", "GET bytes=0-0"}},
		{name: "range not satisfiable", rangeStatus: 416, body: "abc", wantMethods: []string{"HEAD", "GET bytes=0-0", "GET"}},
		{name: "changed length", headLength: "3", getLength: "4", body: "abcd", wantCode: CodeDecodeFailed, wantMethods: []string{"HEAD", "GET"}},
		{name: "truncated body", headLength: "3", getLength: "3", body: "ab", wantCode: CodeDecodeFailed, wantMethods: []string{"HEAD", "GET"}},
		{name: "encoded head", headLength: "3", encoding: "gzip", headEncoded: true, wantCode: CodeDecodeFailed, wantMethods: []string{"HEAD"}},
		{name: "encoded body", headLength: "3", body: "abc", encoding: "gzip", wantCode: CodeDecodeFailed, wantMethods: []string{"HEAD", "GET"}},
		{name: "download rejected", headLength: "3", getStatus: 403, wantCode: CodeDecodeFailed, wantMethods: []string{"HEAD", "GET"}},
		{name: "probe rejected", headStatus: 500, wantCode: CodeDecodeFailed, wantMethods: []string{"HEAD"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			var methods []string
			downloader := newRemoteVideoTLSFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				methods = append(methods, strings.TrimSpace(r.Method+" "+r.Header.Get("Range")))
				mu.Unlock()
				assert.Equal(t, "identity", r.Header.Get("Accept-Encoding"))
				assert.Equal(t, "signature=a%2Fb%2Bc", r.URL.RawQuery)
				assert.Empty(t, r.Header.Get("Authorization"))
				assert.Empty(t, r.Header.Get("Cookie"))
				if r.Method == http.MethodHead {
					if tc.headEncoded {
						w.Header().Set("Content-Encoding", tc.encoding)
					}
					if tc.headLength != "" {
						w.Header().Set("Content-Length", tc.headLength)
					}
					status := tc.headStatus
					if status == 0 {
						status = http.StatusOK
					}
					w.WriteHeader(status)
					if tc.headLength == "" {
						w.(http.Flusher).Flush()
					}
					return
				}
				if r.Header.Get("Range") != "" && !tc.rangeIgnored {
					w.Header().Set("Content-Range", tc.rangeSize)
					status := tc.rangeStatus
					if status == 0 {
						status = http.StatusPartialContent
					}
					w.WriteHeader(status)
					_, _ = io.WriteString(w, "a")
					return
				}
				if tc.encoding != "" {
					w.Header().Set("Content-Encoding", tc.encoding)
				}
				if tc.getLength != "" {
					w.Header().Set("Content-Length", tc.getLength)
				}
				status := tc.getStatus
				if status == 0 {
					status = http.StatusOK
				}
				w.WriteHeader(status)
				if tc.chunked {
					w.(http.Flusher).Flush()
				}
				_, _ = io.WriteString(w, tc.body)
			}))
			cfg := RemoteURLConfig{Enabled: true, AllowUnknownSize: !tc.unknownRejected, DownloadTimeout: 5 * time.Second, downloader: &downloader}
			var stored bytes.Buffer
			result, err := downloadRemoteVideo(context.Background(), "https://video.example.test/movie.mp4?signature=a%2Fb%2Bc", &stored, 4, cfg)
			if tc.wantCode == "" {
				require.NoError(t, err)
				assert.Equal(t, tc.body, stored.String())
				assert.Equal(t, int64(len(tc.body)), result.Bytes)
				assert.Empty(t, result.FailureStage)
			} else {
				require.Error(t, err)
				assert.Equal(t, tc.wantCode, CodeOf(err))
				assert.NotContains(t, err.Error(), "signature")
				assert.NotContains(t, PublicMessageForError(err), "signature")
				assert.NotEmpty(t, result.FailureStage)
			}
			assert.LessOrEqual(t, stored.Len(), 4)
			assert.Equal(t, "video.example.test", result.SourceHost)
			mu.Lock()
			assert.Equal(t, tc.wantMethods, methods)
			mu.Unlock()
		})
	}
}

func TestRemoteVideoURLAndPublicAddressPolicies(t *testing.T) {
	cfg := RemoteURLConfig{Enabled: true, AllowlistEnabled: true, AllowedHosts: []string{"Video.Example.TEST."}}
	for _, tc := range []struct {
		raw     string
		allowed bool
	}{
		{"https://video.example.test/a?signature=secret", true},
		{"https://VIDEO.example.test.:443/a", true},
		{"https://sub.video.example.test/a", false},
		{"https://video.example.test.evil.test/a", false},
		{"https://video.example.test@evil.test/a", false},
		{"https://video.example.test:444/a", false},
		{"https://video.example.test:/a", false},
		{"https://video.example.test/a#fragment", false},
		{"http://video.example.test/a", false},
		{"https://127.0.0.1/a", false},
		{"https://[::1]/a", false},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			target, err := url.Parse(tc.raw)
			require.NoError(t, err)
			err = validateRemoteVideoURL(target, cfg)
			assert.Equal(t, tc.allowed, err == nil)
		})
	}
	cfg.AllowedHosts = nil
	target, err := url.Parse("https://video.example.test/a")
	require.NoError(t, err)
	assert.Error(t, validateRemoteVideoURL(target, cfg), "enabled empty list must deny all")
	cfg.AllowlistEnabled = false
	assert.NoError(t, validateRemoteVideoURL(target, cfg))
	for _, raw := range []string{
		"0.0.0.0", "10.0.0.1", "100.64.0.1", "127.0.0.1", "169.254.169.254", "168.63.129.16", "172.17.0.1", "192.168.1.1",
		"192.0.0.1", "192.0.2.1", "192.88.99.1", "198.18.0.1", "198.51.100.1", "203.0.113.1", "224.0.0.1", "240.0.0.1",
		"::", "::1", "::ffff:127.0.0.1", "::ffff:169.254.169.254", "::192.168.1.1", "fe80::1", "fc00::1", "ff02::1",
		"64:ff9b::a00:1", "64:ff9b:1::a00:1", "100::1", "2001::1", "2001:2::1", "2001:db8::1", "2002:7f00:1::", "3fff::1", "4000::1",
	} {
		assert.False(t, isPublicRemoteVideoIP(netip.MustParseAddr(raw)), raw)
	}
	for _, raw := range []string{"1.1.1.1", "8.8.8.8", "93.184.216.34", "2606:4700::1111", "2001:4860:4860::8888", "::ffff:8.8.8.8"} {
		assert.True(t, isPublicRemoteVideoIP(netip.MustParseAddr(raw)), raw)
	}
}

func TestRemoteVideoRedirectAndDNSValidation(t *testing.T) {
	for _, tc := range []struct {
		name, location    string
		allowlist, rebind bool
		wantCode          string
	}{
		{name: "allowed redirect", location: "https://redirect.example.test/final?token=destination"},
		{name: "allowlist redirect denied", location: "https://redirect.example.test/final", allowlist: true, wantCode: CodeInvalidDataURL},
		{name: "private redirect denied", location: "https://redirect.example.test/final", rebind: true, wantCode: CodeInvalidDataURL},
		{name: "redirect to http", location: "http://video.example.test/final", wantCode: CodeInvalidDataURL},
		{name: "redirect credentials", location: "https://user:secret@video.example.test/final", wantCode: CodeInvalidDataURL},
		{name: "redirect limit", location: "/again", wantCode: CodeInvalidDataURL},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requests atomic.Int32
			downloader := newRemoteVideoTLSFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				assert.Empty(t, r.Referer(), "redirect must not leak signed source URLs")
				assert.Empty(t, r.Header.Get("Cookie"))
				if r.URL.Path != "/final" {
					w.Header().Set("Set-Cookie", "private=secret")
					w.Header().Set("Location", tc.location)
					w.WriteHeader(http.StatusFound)
					return
				}
				w.Header().Set("Content-Length", "3")
				if r.Method == http.MethodGet {
					_, _ = io.WriteString(w, "abc")
				}
			}))
			if tc.rebind {
				downloader.resolver = remoteVideoResolverFunc(func(ctx context.Context, host string) ([]net.IPAddr, error) {
					if host == "redirect.example.test" {
						return []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}, nil
					}
					return []net.IPAddr{{IP: net.ParseIP("93.184.216.34")}}, nil
				})
			}
			cfg := RemoteURLConfig{Enabled: true, DownloadTimeout: 5 * time.Second, AllowUnknownSize: true, AllowlistEnabled: tc.allowlist, AllowedHosts: []string{"video.example.test"}}
			var stored bytes.Buffer
			_, err := downloader.download(context.Background(), "https://video.example.test/start?signature=secret", &stored, 4, cfg)
			if tc.wantCode == "" {
				require.NoError(t, err)
				assert.Equal(t, "abc", stored.String())
			} else {
				require.Error(t, err)
				assert.Equal(t, tc.wantCode, CodeOf(err))
				assert.NotContains(t, err.Error(), "secret")
				if tc.name != "redirect limit" {
					assert.EqualValues(t, 1, requests.Load())
				}
			}
		})
	}

	t.Run("all DNS results must be public", func(t *testing.T) {
		var dials int
		d := remoteVideoDownloader{
			resolver: remoteVideoResolverFunc(func(context.Context, string) ([]net.IPAddr, error) {
				return []net.IPAddr{{IP: net.ParseIP("93.184.216.34")}, {IP: net.ParseIP("10.0.0.1")}}, nil
			}),
			dialContext: func(context.Context, string, string) (net.Conn, error) {
				dials++
				return nil, errors.New("must not dial")
			},
		}
		_, err := d.dialPublicAddress(context.Background(), "tcp", "video.example.test:443")
		require.Error(t, err)
		assert.Equal(t, CodeInvalidDataURL, CodeOf(err))
		assert.Zero(t, dials)
	})
	t.Run("rebinding between HEAD and GET", func(t *testing.T) {
		var requests, resolutions atomic.Int32
		d := newRemoteVideoTLSFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requests.Add(1)
			w.Header().Set("Content-Length", "3")
		}))
		d.resolver = remoteVideoResolverFunc(func(context.Context, string) ([]net.IPAddr, error) {
			if resolutions.Add(1) > 1 {
				return []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}, nil
			}
			return []net.IPAddr{{IP: net.ParseIP("93.184.216.34")}}, nil
		})
		_, err := d.download(context.Background(), "https://video.example.test/a", io.Discard, 4, RemoteURLConfig{Enabled: true, DownloadTimeout: 5 * time.Second})
		require.Error(t, err)
		assert.Equal(t, CodeInvalidDataURL, CodeOf(err))
		assert.EqualValues(t, 1, requests.Load())
	})
}

func TestRemoteVideoCancellationAndTLS(t *testing.T) {
	t.Run("range fallback retains public DNS policy", func(t *testing.T) {
		var requests, resolutions atomic.Int32
		d := newRemoteVideoTLSFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requests.Add(1)
			w.WriteHeader(http.StatusForbidden)
		}))
		d.resolver = remoteVideoResolverFunc(func(ctx context.Context, host string) ([]net.IPAddr, error) {
			if resolutions.Add(1) == 3 {
				return []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}, nil
			}
			return []net.IPAddr{{IP: net.ParseIP("93.184.216.34")}}, nil
		})
		_, err := d.download(context.Background(), "https://video.example.test/a", io.Discard, 4, RemoteURLConfig{Enabled: true, AllowUnknownSize: true, DownloadTimeout: 5 * time.Second})
		assert.Equal(t, CodeInvalidDataURL, CodeOf(err))
		assert.EqualValues(t, 2, requests.Load(), "fallback must not reach the private address")
	})
	t.Run("one deadline includes DNS and every HTTP stage", func(t *testing.T) {
		var mu sync.Mutex
		var deadlines []time.Time
		d := newRemoteVideoTLSFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodHead {
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			if r.Header.Get("Range") != "" {
				w.Header().Set("Content-Range", "bytes 0-0/3")
				w.WriteHeader(http.StatusPartialContent)
				_, _ = io.WriteString(w, "a")
				return
			}
			_, _ = io.WriteString(w, "abc")
		}))
		d.resolver = remoteVideoResolverFunc(func(ctx context.Context, host string) ([]net.IPAddr, error) {
			deadline, present := ctx.Deadline()
			assert.True(t, present)
			mu.Lock()
			deadlines = append(deadlines, deadline)
			mu.Unlock()
			return []net.IPAddr{{IP: net.ParseIP("93.184.216.34")}}, nil
		})
		parentDeadline := time.Now().Add(time.Minute)
		ctx, cancel := context.WithDeadline(context.Background(), parentDeadline)
		defer cancel()
		_, err := d.download(ctx, "https://video.example.test/a", io.Discard, 4, RemoteURLConfig{Enabled: true, DownloadTimeout: 2 * time.Minute})
		require.NoError(t, err)
		mu.Lock()
		assert.Equal(t, []time.Time{parentDeadline, parentDeadline, parentDeadline}, deadlines)
		mu.Unlock()
	})
	t.Run("disabled URL does not resolve", func(t *testing.T) {
		d := remoteVideoDownloader{resolver: remoteVideoResolverFunc(func(context.Context, string) ([]net.IPAddr, error) {
			t.Error("disabled URL must not resolve DNS")
			return nil, errors.New("unexpected DNS")
		})}
		_, err := d.download(context.Background(), "https://video.example.test/a", io.Discard, 4, RemoteURLConfig{DownloadTimeout: time.Second})
		require.Error(t, err)
		assert.Equal(t, CodeInvalidDataURL, CodeOf(err))
	})
	t.Run("cancellation closes active download", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		closed := make(chan struct{})
		d := newRemoteVideoTLSFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Length", "3")
			if r.Method == http.MethodGet {
				w.WriteHeader(http.StatusOK)
				w.(http.Flusher).Flush()
				cancel()
				<-r.Context().Done()
				close(closed)
			}
		}))
		_, err := d.download(ctx, "https://video.example.test/a?signature=secret", io.Discard, 4, RemoteURLConfig{Enabled: true, DownloadTimeout: 5 * time.Second})
		require.Error(t, err)
		assert.Equal(t, CodeProcessingCanceled, CodeOf(err))
		select {
		case <-closed:
		case <-time.After(time.Second):
			t.Fatal("download connection was not canceled")
		}
	})
	t.Run("deadline already expired", func(t *testing.T) {
		ctx, cancel := context.WithDeadline(context.Background(), time.Unix(1, 0))
		defer cancel()
		_, err := downloadRemoteVideo(ctx, "https://video.example.test/a", io.Discard, 4, RemoteURLConfig{Enabled: true, DownloadTimeout: 5 * time.Second})
		require.Error(t, err)
		assert.Equal(t, CodeProcessingTimeout, CodeOf(err))
	})
	t.Run("TLS validates request hostname", func(t *testing.T) {
		d := newRemoteVideoTLSFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("wrong-host certificate must not reach HTTP") }))
		_, err := d.download(context.Background(), "https://wrong.example.test/a?token=secret", io.Discard, 4, RemoteURLConfig{Enabled: true, DownloadTimeout: 5 * time.Second})
		require.Error(t, err)
		assert.Equal(t, CodeDecodeFailed, CodeOf(err))
		assert.NotContains(t, err.Error(), "secret")
	})
	t.Run("environment proxy ignored", func(t *testing.T) {
		t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
		d := newRemoteVideoTLSFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Length", strconv.Itoa(len("abc")))
			if r.Method == http.MethodGet {
				_, _ = io.WriteString(w, "abc")
			}
		}))
		_, err := d.download(context.Background(), "https://video.example.test/a", io.Discard, 4, RemoteURLConfig{Enabled: true, DownloadTimeout: 5 * time.Second})
		require.NoError(t, err)
	})
}
