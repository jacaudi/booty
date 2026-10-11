// Ported from jeefy/booty pkg/hardware/hostname.go @ cae75c8 (MIT).

package hardware

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// MaxHostnameLen is the RFC 1123 limit for a fully qualified name.
const MaxHostnameLen = 253

// maxLabelLen is the RFC 1035 limit for one dotted label.
const maxLabelLen = 63

// ErrInvalidHostname is returned for names that are not RFC 1123 labels or
// dotted names.
var ErrInvalidHostname = errors.New("invalid hostname")

var hostnameRe = regexp.MustCompile(`^(?i)[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`)

// ValidateHostname accepts an RFC 1123 label or dotted name (letters, digits
// and hyphens, no leading/trailing hyphen per label) of at most
// MaxHostnameLen characters. The empty string is rejected; callers that
// treat it as "unset" must check for it first.
func ValidateHostname(name string) error {
	if name == "" {
		return fmt.Errorf("%w: empty", ErrInvalidHostname)
	}
	if len(name) > MaxHostnameLen {
		return fmt.Errorf("%w: %d characters exceeds %d", ErrInvalidHostname, len(name), MaxHostnameLen)
	}
	for label := range strings.SplitSeq(name, ".") {
		if len(label) > maxLabelLen {
			return fmt.Errorf("%w: label %q exceeds 63 characters", ErrInvalidHostname, label)
		}
	}
	if !hostnameRe.MatchString(name) {
		return fmt.Errorf("%w: %q must be an RFC 1123 label or dotted name", ErrInvalidHostname, name)
	}
	return nil
}
