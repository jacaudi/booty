package ostype

import (
	"context"
	"sync"
)

// streamsCache memoizes the FCOS channel streams document within one reconcile
// pass. It is keyed by the fully-resolved streams URL (which encodes channel).
// reconcileTarget resets it at pass entry and reverify resets it before its
// single-version call (D17), so a later pass never resolves a new build against
// a stale doc. Guarded by a mutex because reverify runs on the API goroutine.
var streamsCache = struct {
	sync.Mutex
	docs map[string][]byte
}{docs: map[string][]byte{}}

// fetchStreams returns the streams JSON for url, fetching (and memoizing) it at
// most once between ResetStreamsCache calls.
func fetchStreams(ctx context.Context, url string) ([]byte, error) {
	streamsCache.Lock()
	if b, ok := streamsCache.docs[url]; ok {
		streamsCache.Unlock()
		return b, nil
	}
	streamsCache.Unlock()

	b, err := fetchMetadata(ctx, url)
	if err != nil {
		return nil, err
	}
	streamsCache.Lock()
	streamsCache.docs[url] = b
	streamsCache.Unlock()
	return b, nil
}

// ResetStreamsCache clears the pass-scoped FCOS streams memo. Called at the top
// of every reconcileTarget pass, before each reverify, and by tests (so the
// mandated §10 table tests stay deterministic under -race).
func ResetStreamsCache() {
	streamsCache.Lock()
	clear(streamsCache.docs)
	streamsCache.Unlock()
}
