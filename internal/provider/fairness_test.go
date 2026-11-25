package provider

import "testing"

func TestRollDeterministic(t *testing.T) {
	a := Roll("seed-abc", "client-1", 1)
	b := Roll("seed-abc", "client-1", 1)
	if a != b {
		t.Fatalf("Roll not deterministic: %d != %d", a, b)
	}
	if a < 0 || a > 9999 {
		t.Fatalf("Roll out of range: %d", a)
	}
	// different nonce → (almost surely) different roll
	if a == Roll("seed-abc", "client-1", 2) {
		t.Log("nonce change produced same roll (possible but unlucky)")
	}
	// different seed → different distribution point
	if a == Roll("seed-xyz", "client-1", 1) {
		t.Log("seed change produced same roll (possible but unlucky)")
	}
}

func TestRollDistribution(t *testing.T) {
	buckets := make([]int, 10)
	const n = 20000
	for i := 0; i < n; i++ {
		buckets[Roll("dist-seed", "cs", int64(i))%10]++
	}
	for i, b := range buckets {
		// uniform expectation: 2000 per bucket, allow ±25%
		if b < 1500 || b > 2500 {
			t.Fatalf("bucket %d out of uniform range: %d", i, b)
		}
	}
}

func TestDiceRound(t *testing.T) {
	if win, m := DiceRound(100, 5000); !win || m <= 1.9 || m >= 2.0 {
		t.Fatalf("DiceRound(100,5000) = %v %v, want win ~1.96x", win, m)
	}
	if win, _ := DiceRound(5000, 5000); win {
		t.Fatal("roll == win_under must lose (strict <)")
	}
	if win, _ := DiceRound(9999, 5000); win {
		t.Fatal("roll above threshold must lose")
	}
}

func TestCrashPoint(t *testing.T) {
	if cp := CrashPoint(50); cp != 1.00 {
		t.Fatalf("instant bust broken: %v", cp)
	}
	cp := CrashPoint(5000)
	if cp < 1.5 || cp > 2.1 {
		t.Fatalf("mid roll crash point implausible: %v", cp)
	}
	if cp := CrashPoint(9990); cp <= 50 {
		t.Fatalf("high roll should give big multiplier: %v", cp)
	}
	if cp := CrashPoint(9999); cp != 100 {
		t.Fatalf("cap at 100x broken: %v", cp)
	}
}
