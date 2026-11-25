package sign

import "testing"

func TestPGSignSortedAndDeterministic(t *testing.T) {
	// param order must not matter (sorted inside)
	a := PGSign(map[string]string{
		"operator_token": "tok", "bet_amount": "1", "win_amount": "2",
	}, "secret")
	b := PGSign(map[string]string{
		"win_amount": "2", "operator_token": "tok", "bet_amount": "1",
	}, "secret")
	if a != b {
		t.Fatalf("PGSign not order-independent:\n%s\n%s", a, b)
	}
	if len(a) != 32 {
		t.Fatalf("sign length = %d, want 32 (md5 hex)", len(a))
	}
	// sign input excludes `sign` itself and empty values
	c := PGSign(map[string]string{
		"operator_token": "tok", "bet_amount": "1", "win_amount": "2",
		"sign": "ignored", "empty": "",
	}, "secret")
	if c != a {
		t.Fatalf("sign/empty params must be excluded: %s != %s", c, a)
	}
}

func TestPGVerify(t *testing.T) {
	params := map[string]string{"operator_token": "tok", "trace_id": "x"}
	sig := PGSign(params, "secret")
	if !PGVerify(params, "secret", sig) {
		t.Fatal("valid sign rejected")
	}
	if PGVerify(params, "secret2", sig) {
		t.Fatal("wrong secret accepted")
	}
}
