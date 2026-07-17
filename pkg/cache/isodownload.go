package cache

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
)

// isoClient has NO Timeout: multi-GB ISO transfers can legitimately run far
// longer than config.httpClient's 5-minute ceiling. Cancellation is via ctx
// only (the request carries ctx).
var isoClient = &http.Client{}

// downloadLargeFile streams url to destPath via a destPath+".download"
// in-progress file, resuming from the file's existing size with an HTTP
// Range header when a prior attempt left bytes on disk. The suffix is
// ".download", NOT ".partial" — SweepPartials (partial.go) deletes *.partial
// under the cache root at the top of every reconcile pass, which would
// destroy a resumable multi-GB ISO between ticks. ".download" survives the
// sweep. On success the in-progress file is renamed to destPath.
func downloadLargeFile(ctx context.Context, url, destPath string) error {
	partial := destPath + ".download" // NOT .partial — survives SweepPartials
	f, err := os.OpenFile(partial, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("cache: open %s: %w", partial, err)
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return fmt.Errorf("cache: stat %s: %w", partial, err)
	}
	offset := fi.Size()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("cache: build request %s: %w", url, err)
	}
	if offset > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
	}
	resp, err := isoClient.Do(req)
	if err != nil {
		return fmt.Errorf("cache: download %s: %w", url, err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusPartialContent: // 206 — append
	case http.StatusOK: // 200 — server ignored Range; restart from zero
		if err := f.Truncate(0); err != nil {
			return err
		}
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return err
		}
	default:
		return fmt.Errorf("cache: download %s: unexpected status %s", url, resp.Status)
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		return fmt.Errorf("cache: stream %s: %w", url, err)
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(partial, destPath)
}
