package common

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewReplayableBodyReaderKeepsStorageLifecycleWithCaller(t *testing.T) {
	payload := []byte(`{"model":"test-model","input":"hello"}`)
	storage, err := CreateBodyStorage(payload)
	require.NoError(t, err)
	defer storage.Close()

	body := NewReplayableBodyReader(storage)
	assert.EqualValues(t, len(payload), body.Size())
	_, exposesCloser := any(body).(io.Closer)
	assert.False(t, exposesCloser, "the request body must not expose the storage closer")

	req, err := http.NewRequest(http.MethodPost, "https://example.com", body)
	require.NoError(t, err)
	require.NoError(t, req.Body.Close())

	replayBody, err := body.NewReader()
	require.NoError(t, err, "closing the HTTP request body must not close the storage")
	replay, err := io.ReadAll(replayBody)
	require.NoError(t, err)
	require.NoError(t, replayBody.Close())
	assert.Equal(t, payload, replay)

	require.NoError(t, storage.Close())
	_, err = body.NewReader()
	require.ErrorIs(t, err, ErrStorageClosed)
}

func TestSpilledBodyStoragePreservesCursorReplayAndFailureOwnership(t *testing.T) {
	payload := []byte("0123456789")
	for _, tc := range []struct{ canceled, disk bool }{{false, false}, {true, false}, {false, true}, {true, true}} {
		canceled := tc.canceled
		t.Run(fmt.Sprint(tc), func(t *testing.T) {
			var storage BodyStorage = newMemoryStorage(payload)
			if tc.disk {
				require.NoError(t, storage.Close())
				file, err := os.CreateTemp(t.TempDir(), "original-")
				require.NoError(t, err)
				_, err = file.Write(payload)
				require.NoError(t, err)
				IncrementDiskFiles(int64(len(payload)))
				storage = &diskStorage{file: file, filePath: file.Name(), size: int64(len(payload))}
			}
			defer storage.Close()
			_, err := storage.Seek(3, io.SeekStart)
			require.NoError(t, err)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if canceled {
				cancel()
			}
			closed := 0
			root := t.TempDir()
			err = SpillBodyStorage(ctx, storage, root, func() { closed++ })
			if canceled {
				require.ErrorIs(t, err, context.Canceled)
				assert.Equal(t, tc.disk, storage.IsDisk())
			} else {
				require.NoError(t, err)
				assert.True(t, storage.IsDisk())
			}
			rest, err := io.ReadAll(storage)
			require.NoError(t, err)
			assert.Equal(t, "3456789", string(rest))
			reader, err := storage.NewReader()
			require.NoError(t, err)
			replay, err := io.ReadAll(reader)
			require.NoError(t, err)
			require.NoError(t, reader.Close())
			assert.Equal(t, payload, replay)
			require.NoError(t, storage.Close())
			require.NoError(t, storage.Close())
			if canceled {
				assert.Zero(t, closed)
			} else {
				assert.Equal(t, 1, closed)
			}
			entries, err := os.ReadDir(root)
			require.NoError(t, err)
			assert.Empty(t, entries)
		})
	}
}
