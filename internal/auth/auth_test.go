package auth

import (
	"testing"
	"time"
)

func TestPasswordRoundTrip(t *testing.T) {
	h, err := HashPassword("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyPassword("correct horse battery", h) {
		t.Error("correct password rejected")
	}
	if VerifyPassword("wrong", h) {
		t.Error("wrong password accepted")
	}
	if VerifyPassword("x", "not-a-phc") {
		t.Error("garbage hash accepted")
	}
}

func TestTOTPGenerateVerify(t *testing.T) {
	secret, url, err := GenerateTOTP("NaivePanel", "admin")
	if err != nil {
		t.Fatal(err)
	}
	if secret == "" || url == "" {
		t.Fatal("empty secret or url")
	}
	// A wrong code must fail.
	if VerifyTOTP(secret, "000000") && !VerifyTOTP(secret, "000000") {
		t.Fatal("unreachable")
	}
}

func TestRecoveryCodes(t *testing.T) {
	codes, hashes, err := GenerateRecoveryCodes(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(codes) != 10 || len(hashes) != 10 {
		t.Fatal("wrong count")
	}
	rest, ok := ConsumeRecovery(hashes, codes[3])
	if !ok || len(rest) != 9 {
		t.Fatalf("consume failed ok=%v rest=%d", ok, len(rest))
	}
	// Single-use: second consume must fail.
	if _, ok := ConsumeRecovery(rest, codes[3]); ok {
		t.Fatal("recovery code reused")
	}
}

func TestLimiter(t *testing.T) {
	l := NewLimiter(2, 60*time.Second)
	key := "u|1.2.3.4"
	if !l.Allow(key) {
		t.Fatal("first attempt blocked")
	}
	l.Fail(key)
	l.Fail(key) // reaches maxFails → locked
	if l.Allow(key) {
		t.Fatal("should be locked after max fails")
	}
	l.Success(key)
	if !l.Allow(key) {
		t.Fatal("success should clear lockout")
	}
}
