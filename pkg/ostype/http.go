package ostype

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"
)

// discoveryTimeout bounds a single upstream version-metadata fetch. These are
// small documents (version.txt / streams JSON / factory versions), so the
// ceiling is short. A package var so tests can shrink it.
var discoveryTimeout = 30 * time.Second

// fetchMetadata issues a timeout-bounded GET and returns the full body. A
// non-2xx status or transport/timeout failure is returned as an error.
func fetchMetadata(ctx context.Context, url string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, discoveryTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("ostype: build request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ostype: get %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("ostype: get %s: status %s", url, resp.Status)
	}
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("ostype: read %s: %w", url, err)
	}
	return b, nil
}
