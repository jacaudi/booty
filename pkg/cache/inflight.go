package cache

import "strings"

// IsInFlightPath reports whether p names an in-flight download: a file that is
// incomplete or unverified and must never be served.
//
// Both in-progress suffixes are covered. ".partial" is the staged downloader's
// (pkg/cache/verify.go); DownloadSuffix is the resumable large-file
// downloader's (pkg/cache/isodownload.go), which uses a different suffix precisely so
// SweepPartials cannot delete a multi-GB ISO between reconcile ticks. That
// makes a .download file the LONGER-lived of the two on disk — a stalled
// multi-GB transfer can sit for hours by design — inside exactly the cache
// directory [[baseurl]] points at.
//
// Case-insensitive: both suffixes are always written lowercase, but on a
// case-insensitive dev filesystem (e.g. macOS/APFS) a request for
// "kernel.PARTIAL" would otherwise resolve to the same on-disk file and
// bypass a case-sensitive check. The HTTP and TFTP paths share this predicate.
func IsInFlightPath(p string) bool {
	lower := strings.ToLower(p)
	return strings.HasSuffix(lower, ".partial") || strings.HasSuffix(lower, DownloadSuffix)
}
