package http

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/jeefy/booty/pkg/bootscript"
	"github.com/jeefy/booty/pkg/db"
	"github.com/jeefy/booty/pkg/hardware"
	"github.com/jeefy/booty/pkg/nodereport"
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

	// receiver is set by nodeReportHandler so token rotation can update
	// its generation cache.
	receiver *reportReceiver
}

// nodeReportHandler returns the POST /node/report handler: a 404 when the
// feature is off, else a receiver that is also recorded on deps.NodeReports.
func nodeReportHandler(deps APIDeps) http.Handler {
	if deps.NodeReports == nil {
		return http.NotFoundHandler()
	}
	rr := newReportReceiver(deps.Store, deps.NodeReports.Key, time.Now, arpLookup)
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

// put caches gen. Generations only increase, so a live entry is never
// lowered: a lookup that read the database before a rotation and puts after
// it must not bring the old token back. An expired entry is replaced, which
// lets a deleted and re-added host (generation back at 0) recover.
func (c *genCache) put(mac string, gen int64, exp time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.m[mac]; ok && e.gen > gen && c.now().Before(e.exp) {
		return
	}
	if _, ok := c.m[mac]; !ok && len(c.m) >= genCacheCapacity {
		for k := range c.m { // evict an arbitrary entry
			delete(c.m, k)
			break
		}
	}
	c.m[mac] = genEntry{gen: gen, exp: exp}
}

// reportReceiver serves POST /node/report. It is unauthenticated at the
// HTTP layer: the per-host HMAC token plus an ARP match of the source IP to
// the claimed MAC are its credential, and the per-IP and per-MAC limiters
// bound pre-auth work.
type reportReceiver struct {
	store         *db.Store
	key           []byte
	now           func() time.Time
	perIP, perMAC *nodereport.Limiter
	gens          *genCache
	arp           arpFunc
}

func newReportReceiver(store *db.Store, key []byte, now func() time.Time, arp arpFunc) *reportReceiver {
	return &reportReceiver{
		store:  store,
		key:    key,
		now:    now,
		arp:    arp,
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

// authorize applies the per-IP limit, the token check, the ARP match and the
// per-MAC limit, in that order, and returns [http.StatusOK] or the status to
// reject with.
func (rr *reportReceiver) authorize(r *http.Request, mac, token string) int {
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		ip = r.RemoteAddr
	}
	if !rr.perIP.Allow(ip) {
		return http.StatusTooManyRequests
	}
	gen, err := rr.gens.Lookup(mac)
	if err != nil {
		return http.StatusUnauthorized
	}
	if !nodereport.ValidToken(rr.key, mac, gen, token) {
		return http.StatusUnauthorized
	}
	// ARP sends a packet and can block, so it runs only after the per-IP
	// limit and a valid token, and before the per-MAC limit so a spoofer
	// cannot spend the real host's budget. The token is readable from the
	// host's config, so this check is what ties the report to the machine.
	if !requestFromMAC(r, mac, rr.arp) {
		return http.StatusUnauthorized
	}
	if !rr.perMAC.Allow(mac) {
		return http.StatusTooManyRequests
	}
	return http.StatusOK
}

func (rr *reportReceiver) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
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
	if code := rr.authorize(r, mac, token); code != http.StatusOK {
		rr.reject(w, r, rawMAC, code)
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
	script, err := nodereport.Script(bootscript.URLHost(), host.MAC, nodereport.Token(n.Key, host.MAC, gen))
	if err != nil {
		//nolint:sloglint // default logger: no injected logger in this package yet
		slog.Warn("node report injection skipped", "mac", host.MAC, "err", err)
		return "", "", false
	}
	return script, nodereport.Unit(nodereport.ScriptPath), true
}

// Set stores gen for mac with a fresh TTL (call after rotating its token).
func (c *genCache) Set(mac string, gen int64) {
	c.put(mac, gen, c.now().Add(genCacheTTL))
}
