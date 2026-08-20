package cache

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
)

// isoClient has NO Timeout: multi-GB ISO transfers can legitimately run far
// longer than config.httpClient's 5-minute ceiling. Cancellation is via ctx
// only (the request carries ctx).
var isoClient = &http.Client{}

// DownloadSuffix marks a resumable download still in flight.
//
// It is deliberately NOT ".partial": SweepPartials (partial.go) deletes
// *.partial under the cache root at the top of every reconcile pass, which
// would destroy a resumable multi-GB ISO between ticks. This suffix survives
// the sweep, which also makes such a file the LONGER-lived of the two on disk.
//
// Exported and single-sourced because that one fact has four consumers:
// this file writes it, pkg/http/http.go refuses to serve it, pkg/cache/scan.go
// excludes it from the size total, and pkg/cache/verify.go both writes it
// (landArtifact's Large branch) and reads it (VerifyVersion's in-flight check).
// Four hand-synced literals for one contract is how three of those sites came
// to disagree.
const DownloadSuffix = ".download"

// downloadLargeInto streams url into inProgressPath, resuming from that file's
// existing size with an HTTP Range header when a prior attempt left bytes on
// disk. It returns with the COMPLETE bytes at inProgressPath and deliberately
// does NOT rename: the caller verifies first, then lands (design §5.3). That
// ordering is what keeps an unverified multi-GB file out of the final path
// /data/ serves.
func downloadLargeInto(ctx context.Context, url, inProgressPath string) error {
	f, err := os.OpenFile(inProgressPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("cache: open %s: %w", inProgressPath, err)
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return fmt.Errorf("cache: stat %s: %w", inProgressPath, err)
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
		// The prefix was just discarded, so it is no longer part of this file.
		// Leaving offset stale made the completion log over-report the size by
		// exactly the discarded bytes, which reads as a larger download than
		// actually happened.
		offset = 0
	case http.StatusRequestedRangeNotSatisfiable: // 416 — offset is already past EOF: a
		// crash between io.Copy finishing and the caller's rename on a PRIOR attempt
		// left a full-size in-progress file. The bytes are complete, so this is
		// success — the caller's checksum step is the correctness gate for a truly
		// corrupt file, not this transport layer.
		return f.Close()
	default:
		return fmt.Errorf("cache: download %s: unexpected status %s", url, resp.Status)
	}
	// The staged downloader logs "downloading (staged)"/"staged download
	// complete" around every other artifact. Without the matching pair here
	// the multi-GB transfer — the single longest operation booty performs, and
	// the one on an isoClient with NO timeout — is the only one with zero
	// observability, so an operator cannot distinguish "slow" from "hung".
	slog.Info("downloading (resumable)", "url", url, "resumeOffset", offset)
	n, err := io.Copy(f, resp.Body)
	if err != nil {
		return fmt.Errorf("cache: stream %s: %w", url, err)
	}
	if err := f.Close(); err != nil {
		return err
	}
	slog.Info("resumable download complete", "url", url, "bytes", offset+n)
	return nil
}

// downloadLargeFile is the DEBIAN-DVD-ONLY wrapper: download, then rename, with
// NO verification in between.
//
// Artifact landing does NOT go through here. It goes through downloadLargeInto
// + landArtifact (verify.go), which verifies the completed bytes BEFORE
// renaming. This function survives only for ensureDebianDVD's isoDownload seam,
// which verifies AFTER landing via verifyDVDChecksums and removes the ISOs on
// failure. Do not "simplify" landArtifact back onto it — that reopens the
// unverified-rename hole in one line, and the type system will not stop you.
func downloadLargeFile(ctx context.Context, url, destPath string) error {
	inProgress := destPath + DownloadSuffix
	if err := downloadLargeInto(ctx, url, inProgress); err != nil {
		return err
	}
	return os.Rename(inProgress, destPath)
}
