package http

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/jeefy/booty/pkg/db"
	"github.com/jeefy/booty/pkg/hardware"
	"github.com/jeefy/booty/pkg/nodereport"
	"github.com/jeefy/booty/pkg/tftp"
)

const (
	nodeAuthHeader    = "X-Booty-Node-Token"
	nodeReportMaxBody = 64 << 10
	genCacheTTL       = 60 * time.Second
	genCacheCapacity  = 4096
	limiterCapacity   = 4096
	perIPLimit        = 10
	perMACWindow      = 10 * time.Second
)

// NodeReports configures the node health report receiver. A nil
// *NodeReports on APIDeps leaves the feature off.
type NodeReports struct {
	// Key is the 32-byte HMAC key the per-host report tokens derive from.
	Key []byte

	// receiver is set by nodeReportHandler so token rotation can
	// invalidate its generation cache.
	receiver *reportReceiver
}

// nodeReportHandler returns the POST /node/report handler: a 404 when the
// feature is off, else a receiver that is also recorded on deps.NodeReports.
func nodeReportHandler(deps APIDeps) http.Handler {
	if deps.NodeReports == nil {
		return http.NotFoundHandler()
	}
	rr := newReportReceiver(deps.Store, deps.NodeReports.Key, time.Now)
	deps.NodeReports.receiver = rr
	return rr
}

type genEntry struct {
	gen int64
	exp time.Time
}

// genCache remembers each host's token generation for genCacheTTL so the
// receiver does not hit the database on every report. It holds at most
// genCacheCapacity entries.
type genCache struct {
	mu    sync.Mutex
	m     map[string]genEntry
	store *db.Store
	now   func() time.Time
}

func newGenCache(store *db.Store, now func() time.Time) *genCache {
	return &genCache{m: make(map[string]genEntry), store: store, now: now}
}

// Lookup returns the host's token generation. An absent host is an error
// and is not cached.
func (c *genCache) Lookup(mac string) (int64, error) {
	now := c.now()
	c.mu.Lock()
	e, ok := c.m[mac]
	c.mu.Unlock()
	if ok && now.Before(e.exp) {
		return e.gen, nil
	}
	gen, err := c.store.HostReportTokenGen(mac)
	if err != nil {
		return 0, err
	}
	c.put(mac, gen, now.Add(genCacheTTL))
	return gen, nil
}

func (c *genCache) put(mac string, gen int64, exp time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.m[mac]; !ok && len(c.m) >= genCacheCapacity {
		for k := range c.m { // evict an arbitrary entry
			delete(c.m, k)
			break
		}
	}
	c.m[mac] = genEntry{gen: gen, exp: exp}
}

// Invalidate forgets mac's cached generation (call after rotating its token).
func (c *genCache) Invalidate(mac string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.m, mac)
}

// reportReceiver serves POST /node/report. It is unauthenticated at the
// HTTP layer: the per-host HMAC token is its credential, and the per-IP and
// per-MAC limiters bound pre-auth work.
type reportReceiver struct {
	store         *db.Store
	key           []byte
	now           func() time.Time
	perIP, perMAC *nodereport.Limiter
	gens          *genCache
}

func newReportReceiver(store *db.Store, key []byte, now func() time.Time) *reportReceiver {
	return &reportReceiver{
		store:  store,
		key:    key,
		now:    now,
		perIP:  nodereport.NewLimiter(perIPLimit, time.Minute, limiterCapacity, now),
		perMAC: nodereport.NewLimiter(1, perMACWindow, limiterCapacity, now),
		gens:   newGenCache(store, now),
	}
}

// reject logs at Debug (a flood must not fill the logs) and replies with
// the bare status text, so every 401 body is identical. The token is never
// logged.
func (rr *reportReceiver) reject(w http.ResponseWriter, r *http.Request, rawMAC string, code int) {
	//nolint:sloglint // default logger: no injected logger in this package yet
	slog.Debug("node report rejected", "status", code, "mac", rawMAC, "remote", r.RemoteAddr)
	http.Error(w, http.StatusText(code), code)
}

func (rr *reportReceiver) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
		return
	}
	rawMAC := r.URL.Query().Get("mac")
	mac, err := hardware.NormalizeMAC(rawMAC)
	if err != nil {
		rr.reject(w, r, rawMAC, http.StatusBadRequest)
		return
	}
	token := r.Header.Get(nodeAuthHeader)
	if token == "" {
		rr.reject(w, r, rawMAC, http.StatusUnauthorized)
		return
	}
	ip, _, splitErr := net.SplitHostPort(r.RemoteAddr)
	if splitErr != nil {
		ip = r.RemoteAddr
	}
	if !rr.perIP.Allow(ip) {
		rr.reject(w, r, rawMAC, http.StatusTooManyRequests)
		return
	}
	gen, err := rr.gens.Lookup(mac)
	if err != nil {
		rr.reject(w, r, rawMAC, http.StatusUnauthorized)
		return
	}
	if !nodereport.ValidToken(rr.key, mac, gen, token) {
		rr.reject(w, r, rawMAC, http.StatusUnauthorized)
		return
	}
	if !rr.perMAC.Allow(mac) {
		rr.reject(w, r, rawMAC, http.StatusTooManyRequests)
		return
	}
	var h nodereport.Health
	body := http.MaxBytesReader(w, r.Body, nodeReportMaxBody)
	if err = json.NewDecoder(body).Decode(&h); err != nil {
		if _, tooBig := errors.AsType[*http.MaxBytesError](err); tooBig {
			http.Error(w, http.StatusText(http.StatusRequestEntityTooLarge), http.StatusRequestEntityTooLarge)
			return
		}
		rr.reject(w, r, rawMAC, http.StatusBadRequest)
		return
	}
	if err = h.Normalize(); err != nil {
		rr.reject(w, r, rawMAC, http.StatusBadRequest)
		return
	}
	h.ReceivedAt = rr.now().UTC().Format(time.RFC3339)
	b, err := json.Marshal(h)
	if err != nil {
		rr.reject(w, r, rawMAC, http.StatusBadRequest)
		return
	}
	if err = rr.store.UpsertHostHealth(mac, h.ReceivedAt, b); err != nil {
		// The host was deleted between the token check and the write.
		rr.reject(w, r, rawMAC, http.StatusUnauthorized)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// script builds the health-report script and unit for host. The bool is false
// when the feature is off, the host is unknown, the generation lookup fails
// or the inputs cannot be embedded safely; the caller then serves the config
// unchanged.
func (n *NodeReports) script(store *db.Store, host *hardware.Host) (string, string, bool) {
	if n == nil || host == nil {
		return "", "", false
	}
	gen, err := store.HostReportTokenGen(host.MAC)
	if err != nil {
		return "", "", false
	}
	script, err := nodereport.Script(tftp.URLHost(), host.MAC, nodereport.Token(n.Key, host.MAC, gen))
	if err != nil {
		//nolint:sloglint // default logger: no injected logger in this package yet
		slog.Warn("node report injection skipped", "mac", host.MAC, "err", err)
		return "", "", false
	}
	return script, nodereport.Unit(nodereport.ScriptPath), true
}
