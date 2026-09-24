package video_preprocess

import (
	"context"
	"encoding/base64"
	"io"
	"math/bits"
	"os"
	"strings"
)

// VideoInput is one video Data URL supplied by the caller. The engine never
// keeps a decoded copy of it in memory and never writes it under a name taken
// from the client.
type VideoInput struct {
	// URL is the full data URL, for example:
	//   data:video/mp4;base64,AAAAIGZ0eXBpc29t...
	URL string

	// Index is the caller's own ordinal for this video. It is copied into
	// Result.Videos so a caller can map frames back to the content block it
	// extracted the video from, and is never used to build a path.
	Index int
}

// videoDataURL is the parsed shape of a video Data URL.
type videoDataURL struct {
	mime      string
	parameter string
	payload   string

	// padded reports whether the base64 payload ended in '='. Padded and
	// unpadded payloads are both accepted; see SizeHint.
	padded bool
}

// SizeHint returns the exact number of bytes the payload decodes to. The
// value is derived from the payload length, so it is available without
// decoding anything.
func (u videoDataURL) SizeHint() int64 {
	if u.padded {
		padding := len(u.payload) - len(strings.TrimRight(u.payload, "="))
		return int64(base64.StdEncoding.DecodedLen(len(u.payload)) - padding)
	}
	return int64(base64.StdEncoding.WithPadding(base64.NoPadding).DecodedLen(len(u.payload)))
}

// parseVideoDataURL strictly parses a Base64 video Data URL.
//
// Rules:
//   - the scheme must be "data:";
//   - the media type must carry a base64 marker and a video/* MIME type;
//   - the MIME type must be in the caller's allowlist when one is supplied;
//   - remote URLs (http, https, rtsp, ...) are rejected: V1 deliberately does
//     not download customer video, which keeps SSRF out of the request path;
//   - the payload must be valid, canonical base64.
//
// It returns the decoded payload length estimate, not the payload itself.
func parseVideoDataURL(raw string, allowedMIMEs []string) (videoDataURL, int64, error) {
	if raw == "" {
		return videoDataURL{}, 0, newError(CodeInvalidDataURL, "video url is empty")
	}
	if !strings.HasPrefix(raw, "data:") {
		return videoDataURL{}, 0, newError(CodeInvalidDataURL, "video url is not a data URL")
	}

	// Find the payload separator. The payload may contain any base64 character
	// but never a second comma, so the first one wins and later commas stay in
	// the payload where they are rejected by the decoder.
	comma := strings.IndexByte(raw, ',')
	if comma < 0 {
		return videoDataURL{}, 0, newError(CodeInvalidDataURL, "data URL is missing its payload separator")
	}
	payload := raw[comma+1:]
	if payload == "" {
		return videoDataURL{}, 0, newError(CodeInvalidDataURL, "data URL payload is empty")
	}

	meta := raw[len("data:"):comma]
	parts := strings.Split(meta, ";")
	mediaType := parts[0]
	if mediaType == "" {
		return videoDataURL{}, 0, newError(CodeInvalidDataURL, "data URL is missing a media type")
	}

	base64Encoded := false
	var parameter string
	for _, p := range parts[1:] {
		trimmed := strings.TrimSpace(p)
		if equalFoldASCII(trimmed, "base64") {
			base64Encoded = true
			continue
		}
		if parameter == "" {
			parameter = trimmed
		}
	}
	if !base64Encoded {
		return videoDataURL{}, 0, newError(CodeInvalidDataURL, "only base64 data URLs are supported")
	}
	// A non-base64 parameter such as "charset=utf-8" on a video data URL is a
	// sign the payload is not what it claims to be.
	if parameter != "" {
		return videoDataURL{}, 0, newError(CodeInvalidDataURL, "unsupported data URL parameter")
	}

	mime := strings.ToLower(strings.TrimSpace(mediaType))
	group, _, ok := strings.Cut(mime, "/")
	if !ok {
		return videoDataURL{}, 0, newError(CodeInvalidDataURL, "data URL media type is not a MIME type")
	}
	if group != "video" {
		return videoDataURL{}, 0, newError(CodeUnsupportedFormat, "data URL media type is not a video type")
	}
	if len(allowedMIMEs) > 0 && !containsFold(allowedMIMEs, mime) {
		return videoDataURL{}, 0, newError(CodeUnsupportedFormat, "video MIME type is not enabled")
	}

	u := videoDataURL{mime: mime, parameter: parameter, payload: payload}
	u.padded = strings.HasSuffix(payload, "=")
	if !u.padded {
		// Accept unpadded payloads too, but require the alphabet to be intact
		// so a stray character is a decode failure and not silently dropped.
		if n := len(payload) % 4; n == 1 {
			return videoDataURL{}, 0, newError(CodeInvalidBase64, "base64 payload length is not valid")
		}
	} else if len(payload)%4 != 0 {
		return videoDataURL{}, 0, newError(CodeInvalidBase64, "padded base64 payload length is not a multiple of four")
	}

	return u, u.SizeHint(), nil
}

// decodeVideoDataURL decodes the payload of u into dst, refusing to write more
// than maxBytes. The base64 text is decoded in blocks so the caller never
// holds a second full copy of the video in memory and the decoded video never
// lands anywhere except dst.
//
// It returns video_too_large as soon as the bound is crossed, before the
// offending block is written.
func decodeVideoDataURL(ctx context.Context, u videoDataURL, dst io.Writer, maxBytes int64) (int64, error) {
	if hint := u.SizeHint(); hint > maxBytes {
		return 0, newError(CodeTooLarge, "decoded video exceeds the decoded size limit")
	}

	payload := u.payload
	const blockChars = 256 * 1024 // multiple of 4, roughly 192 KiB decoded

	var (
		written int64
		buf     = make([]byte, base64.StdEncoding.DecodedLen(blockChars)+4)
	)
	for len(payload) > 0 {
		if err := timeoutOrCancel(ctx); err != nil {
			return written, err
		}
		take := min(blockChars, len(payload))
		// Never split a base64 quantum across blocks.
		if take < len(payload) && take%4 != 0 {
			take -= take % 4
		}
		if take == 0 {
			take = len(payload)
		}
		chunk := payload[:take]
		payload = payload[take:]

		// Padded base64 may only terminate the payload. A '=' inside a chunk
		// signals either corruption or a padded group straddling blocks.
		if strings.ContainsRune(chunk, '=') && len(payload) > 0 {
			return written, newError(CodeInvalidBase64, "base64 padding appears before the end of the payload")
		}

		if strings.ContainsAny(chunk, "\r\n") {
			return written, newError(CodeInvalidBase64, "video payload is not canonical base64")
		}
		decoder := base64.RawStdEncoding.Strict()
		if len(payload) == 0 && u.padded {
			decoder = base64.StdEncoding.Strict()
		}
		n, err := decoder.Decode(buf, []byte(chunk))
		if err != nil {
			return written, wrapError(CodeInvalidBase64, "video payload is not valid base64", err)
		}
		if written+int64(n) > maxBytes {
			return written, newError(CodeTooLarge, "decoded video exceeds the decoded size limit")
		}
		count, err := dst.Write(buf[:n])
		if err == nil && count != n {
			err = io.ErrShortWrite
		}
		if err != nil {
			return written, wrapError(CodeStorageFailed, "failed to write the decoded video", err)
		}
		written += int64(n)
	}
	if written == 0 {
		return 0, newError(CodeInvalidBase64, "video payload decoded to zero bytes")
	}
	return written, nil
}

// sniffContainer returns a rough container label from the leading magic bytes
// of a decoded video. It is a cheap sanity signal used only to reject media
// whose declared MIME type contradicts its content before ffprobe is spawned.
func sniffContainer(head []byte) string {
	switch {
	case len(head) >= 12 && string(head[4:8]) == "ftyp":
		return "mov"
	case len(head) >= 4 && string(head[0:4]) == "\x1a\x45\xdf\xa3":
		_, header, _, ok := readEBMLElement(head)
		if !ok {
			return ""
		}
		var docType string
		for len(header) > 0 {
			id, value, rest, valid := readEBMLElement(header)
			if !valid {
				return ""
			}
			if id == 0x4282 { // EBML DocType, not the shared demuxer name.
				if docType != "" || (string(value) != "webm" && string(value) != "matroska") {
					return ""
				}
				docType = string(value)
			}
			header = rest
		}
		return docType
	case len(head) >= 12 && string(head[0:4]) == "RIFF" && string(head[8:12]) == "AVI ":
		return "avi"
	case len(head) >= 4 && string(head[0:4]) == "FLV\x01":
		return "flv"
	default:
		return ""
	}
}

// readEBMLElement only accepts fully contained, known-size elements. The caller
// supplies a bounded header, so neither sizes nor allocations trust the file.
func readEBMLElement(data []byte) (uint64, []byte, []byte, bool) {
	if len(data) == 0 || data[0] == 0 {
		return 0, nil, nil, false
	}
	idBytes := 9 - bits.Len8(data[0])
	if idBytes > 4 || len(data) <= idBytes || data[idBytes] == 0 {
		return 0, nil, nil, false
	}
	var id uint64
	for _, b := range data[:idBytes] {
		id = id<<8 | uint64(b)
	}
	sizeBytes := 9 - bits.Len8(data[idBytes])
	if len(data) < idBytes+sizeBytes {
		return 0, nil, nil, false
	}
	size := uint64(data[idBytes] & (0xff >> sizeBytes))
	for _, b := range data[idBytes+1 : idBytes+sizeBytes] {
		size = size<<8 | uint64(b)
	}
	data = data[idBytes+sizeBytes:]
	if size == uint64(1)<<(7*sizeBytes)-1 || size > uint64(len(data)) {
		return 0, nil, nil, false
	}
	return id, data[:size], data[size:], true
}

// MIME/container matching is shared by Data URLs and downloaded files. MP4
// and MOV retain their existing family behavior; MKV is not WebM.
func videoContainerForMIME(mime string) string {
	switch strings.ToLower(mime) {
	case "video/mp4", "video/quicktime":
		return "mov"
	case "video/webm":
		return "webm"
	case "video/x-matroska":
		return "matroska"
	case "video/x-msvideo", "video/avi":
		return "avi"
	case "video/x-flv":
		return "flv"
	default:
		return ""
	}
}

// readHead reads at most n leading bytes from path without consuming or
// retaining the rest of the file.
func readHead(path string, n int) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	buf := make([]byte, n)
	read, err := io.ReadFull(f, buf)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return nil, err
	}
	return buf[:read], nil
}
