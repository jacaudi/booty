package versions

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/jeefy/booty/pkg/config"
	"github.com/spf13/viper"
)

func TestSelectRetained(t *testing.T) {
	cases := []struct {
		name string
		tags []string
		n    int
		want []string
	}{
		{"latest patch per latest 3 minors", []string{"v1.10.3", "v1.10.5", "v1.9.7", "v1.9.6", "v1.8.4", "v1.7.0"}, 3, []string{"v1.10.5", "v1.9.7", "v1.8.4"}},
		{"drops prereleases", []string{"v1.10.5", "v1.10.6-beta.0", "v1.9.7"}, 3, []string{"v1.10.5", "v1.9.7"}},
		{"drops malformed tags", []string{"v1.10.5", "not-a-version", "", "v1.9.7"}, 3, []string{"v1.10.5", "v1.9.7"}},
		{"empty input", []string{}, 3, []string{}},
		{"fewer minors than n", []string{"v1.10.5", "v1.10.4"}, 3, []string{"v1.10.5"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := selectRetained(tc.tags, tc.n)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("selectRetained(%v, %d) = %v, want %v", tc.tags, tc.n, got, tc.want)
			}
		})
	}
}

func TestNewestCachedTalos(t *testing.T) {
	viper.Reset()
	root := t.TempDir()
	viper.Set(config.DataDir, root)
	for _, v := range []string{"v1.9.7", "v1.10.5", "v1.10.3"} {
		if err := os.MkdirAll(cacheDir("talos", "abc", "amd64", v), 0o755); err != nil {
			t.Fatalf("seed %s: %v", v, err)
		}
	}
	if got := NewestCachedTalos("abc", "amd64"); got != "v1.10.5" {
		t.Errorf("NewestCachedTalos = %q, want v1.10.5", got)
	}
	if got := NewestCachedTalos("missing", "amd64"); got != "" {
		t.Errorf("NewestCachedTalos(missing) = %q, want empty", got)
	}
}

func TestTalosFetchVersions(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if filepath.Base(r.URL.Path) != "versions" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`["v1.10.5","v1.9.7"]`))
	}))
	defer srv.Close()
	viper.Reset()
	viper.Set(config.TalosFactoryURL, srv.URL)
	got, err := fetchTalosVersions()
	if err != nil {
		t.Fatalf("fetchTalosVersions: %v", err)
	}
	want := []string{"v1.10.5", "v1.9.7"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("fetchTalosVersions = %v, want %v", got, want)
	}
}
