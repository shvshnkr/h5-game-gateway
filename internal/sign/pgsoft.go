package sign

import (
	"crypto/md5"
	"encoding/hex"
	"net/url"
	"sort"
	"strings"
)

// PG Soft agent-API signature (classic scheme):
//
//	sign = md5_hex( sorted("key=urlencoded(value)" joined with "&") + secret_key )
//
// The `sign` parameter itself is excluded from the input; empty values are
// skipped. Contrast with the GSC-style scheme (Compute above): PG signs the
// whole sorted parameter set, GSC signs only time+secret+method.
func PGSign(params map[string]string, secretKey string) string {
	keys := make([]string, 0, len(params))
	for k, v := range params {
		if k == "sign" || v == "" {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+url.QueryEscape(params[k]))
	}
	h := md5.Sum([]byte(strings.Join(parts, "&") + secretKey))
	return hex.EncodeToString(h[:])
}

// PGVerify checks a PG-style signature (constant-time).
func PGVerify(params map[string]string, secretKey, want string) bool {
	return hmacEqual([]byte(PGSign(params, secretKey)), []byte(want))
}
