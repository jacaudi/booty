package tftp

import (
	"path"
	"slices"
	"strings"

	"github.com/spf13/viper"

	"github.com/jeefy/booty/pkg/cache"
	"github.com/jeefy/booty/pkg/config"
)

// publicDir is the dataDir subtree served to clients without further checks.
const publicDir = "public"

// allowedDataFile reports whether a TFTP-requested name may be served from
// dataDir. It mirrors the /data/ allowlist in pkg/http (isAllowedDataPath,
// pkg/http/http.go): dataDir also holds api-token, booty.db and config/, which
// must never leave the host. Allowed: the cache/ and public/ subtrees (minus
// in-flight downloads) and the exact top-level names in topLevel.
func allowedDataFile(name string, topLevel []string) bool {
	if name == "" {
		return false
	}
	// TFTP names are slash-separated, so use path, not filepath.
	cleaned := path.Clean("/" + name)
	if cache.IsInFlightPath(cleaned) {
		return false
	}
	if strings.HasPrefix(cleaned, "/"+cache.DirName+"/") || strings.HasPrefix(cleaned, "/"+publicDir+"/") {
		return true
	}
	return slices.Contains(topLevel, strings.TrimPrefix(cleaned, "/")) && strings.Count(cleaned, "/") == 1
}

// TopLevelBootFiles returns the iPXE bootfile names served from the top level
// of dataDir: the fixed defaults plus any configured proxyDHCP bootfiles.
func TopLevelBootFiles() []string {
	names := []string{"undionly.kpxe", "ipxe.efi", "snponly.efi", "ipxe-arm64.efi"}
	for _, key := range []string{config.ProxyDHCPBootfileBIOS, config.ProxyDHCPBootfileUEFI, config.ProxyDHCPBootfileARM64} {
		if v := viper.GetString(key); v != "" {
			names = append(names, v)
		}
	}
	slices.Sort(names)
	return slices.Compact(names)
}
