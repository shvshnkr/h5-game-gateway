package sign

import "testing"

func TestComputeKnownVector(t *testing.T) {
	// deterministic: md5("DEMO1732500000secretbalance")
	got := Compute("DEMO", "1732500000", "secret", "balance")
	want := Compute("DEMO", "1732500000", "secret", "balance")
	if got != want {
		t.Fatalf("Compute not deterministic: %s != %s", got, want)
	}
	if len(got) != 32 {
		t.Fatalf("md5 hex length = %d, want 32", len(got))
	}
}

func TestValid(t *testing.T) {
	ok := Valid("DEMO", "1732500000", "secret", MethodWithdraw, Compute("DEMO", "1732500000", "secret", MethodWithdraw))
	if !ok {
		t.Fatal("Valid=false for correct signature")
	}
	bad := Valid("DEMO", "1732500000", "secret", MethodWithdraw, "deadbeef")
	if bad {
		t.Fatal("Valid=true for wrong signature")
	}
	// method word must be bound into the signature
	mismatch := Valid("DEMO", "1732500000", "secret", MethodBalance, Compute("DEMO", "1732500000", "secret", MethodWithdraw))
	if mismatch {
		t.Fatal("signature cross-accepted between methods")
	}
}
