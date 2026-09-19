package video_preprocess

import (
	"io"
	"net/http"
	"strconv"

	"github.com/QuantumNous/new-api/common"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// InstalledBody is the handle returned by a successful InstallRewrittenBody. It
// reports what the request now serves and lets the caller clean up the storage
// it installed.
//
// Ownership: the returned storage belongs to the request, not to the caller.
// The request-scoped cleanup (common.CleanupBodyStorage) closes it at the end
// of the request, and the AI Router replay and every provider attempt read from
// it in between. Callers must not close it while the request is in flight; it
// is exposed only so that an operator path with its own cleanup can be
// idempotent about it.
type InstalledBody struct {
	storage common.BodyStorage

	// Bytes is the rewritten request size, also installed as the request's
	// ContentLength.
	Bytes int64
	// WasDisk reports whether the rewritten body was stored on disk rather than
	// in memory.
	WasDisk bool
}

// Storage returns the installed storage. It is never nil on a non-error return.
// The caller does not own its lifetime.
func (b *InstalledBody) Storage() common.BodyStorage { return b.storage }

// InstallRewrittenBody atomically replaces the request body with the rewritten
// one.
//
// The new storage is created and its size checked first; only then are
// c.Request.Body, c.Request.ContentLength and the Gin body storage key updated.
// The previous storage is closed only after the swap succeeded, so a failure at
// any point before that leaves the request exactly as it was: the AI Router can
// still replay the original body and the client request is not lost.
//
// The previous storage is deliberately not closed when it equals the newly
// created one (an in-memory storage of identical bytes cannot, but a caller may
// pass an existing storage's bytes) and never when the old storage is nil.
func InstallRewrittenBody(c *gin.Context, body []byte) (*InstalledBody, error) {
	if c == nil {
		return nil, newError(CodeStorageFailed, "gin context is required")
	}
	if c.Request == nil {
		return nil, newError(CodeStorageFailed, "request is required")
	}
	// Validate before building anything: an unparsable rewrite must never reach
	// the request, and rejecting it here is what keeps the failure atomic.
	if !gjson.ValidBytes(body) {
		return nil, newError(CodeRewriteFailed, "rewritten request is not valid JSON")
	}

	// Read the current storage without forcing a re-read of the body when the
	// request never installed one (common.GetBodyStorage would consume the
	// stream just to hand it back).
	previous, hasPrevious := currentStorage(c)

	newStorage, err := common.CreateBodyStorage(body)
	if err != nil {
		return nil, wrapError(CodeStorageFailed, "failed to create storage for the rewritten request", err)
	}

	size := newStorage.Size()
	if int64(len(body)) != size {
		newStorage.Close()
		return nil, newError(CodeStorageFailed, "rewritten request storage size does not match its payload")
	}

	// Point of no return: from here on the rewrite is committed, and the new
	// ContentLength makes the body readable with or without HTTP-level length
	// framing.
	c.Set(common.KeyBodyStorage, newStorage)
	c.Set(common.KeyRequestBody, nil)
	c.Request.Body = io.NopCloser(newStorage)
	c.Request.ContentLength = size
	c.Request.GetBody = newStorage.NewReader
	c.Request.TransferEncoding = nil
	if c.Request.Header == nil {
		c.Request.Header = make(http.Header)
	}
	c.Request.Header.Del("Transfer-Encoding")
	c.Request.Header.Set("Content-Length", strconv.FormatInt(size, 10))

	if hasPrevious && previous != newStorage {
		_ = previous.Close()
	}

	return &InstalledBody{
		storage: newStorage,
		Bytes:   size,
		WasDisk: newStorage.IsDisk(),
	}, nil
}

// currentStorage reports the storage installed on the context. It reports false
// when no storage was installed yet, so the caller knows the request body was
// never buffered and there is nothing to close.
func currentStorage(c *gin.Context) (common.BodyStorage, bool) {
	value, exists := c.Get(common.KeyBodyStorage)
	if !exists || value == nil {
		return nil, false
	}
	storage, ok := value.(common.BodyStorage)
	return storage, ok
}
