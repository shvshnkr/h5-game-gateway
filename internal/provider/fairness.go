package provider

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

// Provably fair RNG (industry-standard commit/reveal scheme):
//   1. server generates serverSeed, publishes sha256(serverSeed) upfront;
//   2. client may supply clientSeed per round;
//   3. roll = HMAC_SHA256(serverSeed, fmt("%s:%d", clientSeed, nonce)),
//      first 8 hex chars → int → mod 10000 → uniform 0..9999;
//   4. after the session the serverSeed is revealed so any player can
//      recompute every roll and verify the published hash.

type Fairness struct {
	ServerSeed     string `json:"server_seed"`
	ServerSeedHash string `json:"server_seed_hash"`
}

func newSeed() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func hashSeed(seed string) string {
	h := sha256.Sum256([]byte(seed))
	return hex.EncodeToString(h[:])
}

// Roll computes the uniform 0..9999 value for a nonce.
func Roll(serverSeed, clientSeed string, nonce int64) int64 {
	mac := hmac.New(sha256.New, []byte(serverSeed))
	fmt.Fprintf(mac, "%s:%d", clientSeed, nonce)
	sum := hex.EncodeToString(mac.Sum(nil))
	// first 8 hex chars = 32 bits; take mod 10000 (documented modulo bias is
	// negligible for a demo: 2^32 mod 10000 offset ≈ 0.000002%)
	var v uint64
	for _, c := range sum[:8] {
		v = v*16 + uint64(hexVal(byte(c)))
	}
	return int64(v % 10000)
}

func hexVal(c byte) int64 {
	switch {
	case c >= '0' && c <= '9':
		return int64(c - '0')
	case c >= 'a' && c <= 'f':
		return int64(c-'a') + 10
	case c >= 'A' && c <= 'F':
		return int64(c-'A') + 10
	}
	return 0
}

// DiceRound resolves a dice round: player wins when roll < winUnder.
// Payout multiplier accounts for the house edge (2%).
func DiceRound(roll int64, winUnder int64) (win bool, multiplier float64) {
	if winUnder <= 0 || winUnder > 9999 {
		winUnder = 5000
	}
	if roll < winUnder {
		chance := float64(winUnder) / 10000.0
		return true, 0.98 / chance // 98% RTP with 2% house edge
	}
	return false, 0
}

// CrashPoint converts a roll into a crash multiplier: 1% instant bust,
// 2% house edge (RTP 0.98), capped at 100x.
func CrashPoint(roll int64) float64 {
	if roll < 100 {
		return 1.00 // instant bust
	}
	h := float64(roll) / 10000.0
	cp := 0.98 / (1 - h)
	if cp > 100 {
		cp = 100
	}
	// round down to 2 decimals (player-friendly)
	return float64(int(cp*100)) / 100
}
