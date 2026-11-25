// Package sign implements the shared MD5 request-signing scheme used
// between the game provider and the operator gateway.
//
// sign = hex(md5(operator_code + request_time + secret_key + method_word))
//
// This mirrors the agent/seamless-wallet APIs used by slot aggregators
// (PG-Soft-style): every callback carries operator_code, request_time and a
// signature; the server recomputes it from its copy of the secret key.
package sign

import (
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"time"
)

// Method words are appended into the signature so that a captured request
// for one endpoint cannot be replayed against another.
const (
	MethodBalance     = "balance"
	MethodWithdraw    = "withdraw"
	MethodDeposit     = "deposit"
	MethodPushBetData = "pushbetdata"
	MethodLaunch      = "launch"
	MethodGames       = "games"
	MethodWebhook     = "webhook"
	MethodFairness    = "fairness"
)

// Compute returns the hex signature for the given parameters.
func Compute(operatorCode, requestTime, secretKey, methodWord string) string {
	h := md5.Sum([]byte(operatorCode + requestTime + secretKey + methodWord))
	return hex.EncodeToString(h[:])
}

// Now returns the current unix timestamp as the string form used in
// request_time fields.
func Now() string {
	return strconv.FormatInt(time.Now().Unix(), 10)
}

// ComputeSHA256 returns the hex sha256 signature — used by the payment
// processor webhook (WebCrypto on the client cannot do MD5; the processor
// contract is ours to define, unlike the aggregator md5 scheme).
func ComputeSHA256(operatorCode, requestTime, secretKey, methodWord string) string {
	h := sha256.Sum256([]byte(operatorCode + requestTime + secretKey + methodWord))
	return hex.EncodeToString(h[:])
}

// ValidSHA256 is the constant-time check for ComputeSHA256 outputs.
func ValidSHA256(operatorCode, requestTime, secretKey, methodWord, want string) bool {
	return hmacEqual([]byte(ComputeSHA256(operatorCode, requestTime, secretKey, methodWord)), []byte(want))
}

// Valid is a constant-time comparison helper. It returns true when want
// matches the computed signature for the supplied parameters.
func Valid(operatorCode, requestTime, secretKey, methodWord, want string) bool {
	return hmacEqual([]byte(Compute(operatorCode, requestTime, secretKey, methodWord)), []byte(want))
}

// hmacEqual is a tiny constant-time string equality (avoids importing
// crypto/subtle at every call site).
func hmacEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	var v byte
	for i := 0; i < len(a); i++ {
		v |= a[i] ^ b[i]
	}
	return v == 0
}
