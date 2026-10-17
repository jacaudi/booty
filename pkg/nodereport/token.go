package nodereport

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
)

const keyLen = 32

// Token derives the per-host report token: lowercase hex of
// HMAC-SHA256(key, "booty-node-report\x00" + mac + "\x00" + gen).
//
// It returns "" when key is not keyLen bytes, which ValidToken rejects.
func Token(key []byte, mac string, gen int64) string {
	if len(key) != keyLen {
		return ""
	}
	m := hmac.New(sha256.New, key)
	m.Write([]byte("booty-node-report\x00" + mac + "\x00" + strconv.FormatInt(gen, 10)))
	return hex.EncodeToString(m.Sum(nil))
}

// ValidToken reports whether presented is the token for mac and gen, in
// constant time.
func ValidToken(key []byte, mac string, gen int64, presented string) bool {
	want := Token(key, mac, gen)
	if want == "" {
		return false
	}
	return hmac.Equal([]byte(want), []byte(presented))
}
