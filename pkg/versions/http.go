package versions

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"
)

// versionCheckTimeout bounds a single remote version-check request. These are
// small metadata fetches (a version.txt / streams JSON), so the ceiling is
// short — not the 5-minute config.DownloadFile ceiling used for artifacts.
// It is a package var (not a const) so tests can shrink it.
var versionCheckTimeout = 30 * time.Second

// versionCheckClient is the package-level client reused by every remote
// version check. It carries no Timeout of its own: the per-call context
// deadline in fetchVersionMetadata is the single source of truth, since it
// reads versionCheckTimeout live (a client.Timeout would freeze the value at
// init and decouple from the test-overridable var). Every request path goes
// through fetchVersionMetadata, so the context deadline always applies.
var versionCheckClient = &http.Client{}

// fetchVersionMetadata issues a timeout-bounded GET for url and returns the
// fully read response body. A non-2xx status or any transport/timeout failure
// is returned as an error so callers don't parse a partial or error body.
func fetchVersionMetadata(url string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), versionCheckTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("versions: build request: %w", err)
	}

	resp, err := versionCheckClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("versions: get %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("versions: get %s: status %s", url, resp.Status)
	}

	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("versions: read %s: %w", url, err)
	}
	return b, nil
}
