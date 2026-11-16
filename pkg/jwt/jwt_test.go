package jwt

import (
	"net/http/httptest"
	"testing"
	"time"

	gojwt "github.com/golang-jwt/jwt/v4"
)

func TestGenerateAndValidate(t *testing.T) {
	h := New("secret")
	token, err := h.GenerateJWT(DefaultClaims(OptionId("agent-1")))
	if err != nil {
		t.Fatal(err)
	}

	claims, err := h.ValidateToken(token)
	if err != nil {
		t.Fatalf("validate: %s", err)
	}
	if claims.Id != "agent-1" {
		t.Fatalf("id = %q, want agent-1", claims.Id)
	}
}

func TestDefaultClaimsExpireInADay(t *testing.T) {
	c := DefaultClaims()
	if c.Id != "" {
		t.Fatalf("id = %q, want empty", c.Id)
	}
	left := time.Until(c.ExpiresAt.Time)
	if left < 23*time.Hour || left > 24*time.Hour {
		t.Fatalf("expires in %s, want about 24h", left)
	}
}

func TestValidateRejects(t *testing.T) {
	h := New("secret")
	good, err := h.GenerateJWT(DefaultClaims())
	if err != nil {
		t.Fatal(err)
	}
	otherKey, err := New("other").GenerateJWT(DefaultClaims())
	if err != nil {
		t.Fatal(err)
	}
	expired, err := h.GenerateJWT(&Claims{RegisteredClaims: gojwt.RegisteredClaims{
		ExpiresAt: gojwt.NewNumericDate(time.Now().Add(-time.Minute)),
	}})
	if err != nil {
		t.Fatal(err)
	}
	// alg "none" must never be accepted, whatever the key.
	none, err := gojwt.NewWithClaims(gojwt.SigningMethodNone, DefaultClaims()).
		SignedString(gojwt.UnsafeAllowNoneSignatureType)
	if err != nil {
		t.Fatal(err)
	}

	for name, token := range map[string]string{
		"empty":     "",
		"garbage":   "not.a.jwt",
		"other key": otherKey,
		"expired":   expired,
		"alg none":  none,
		"tampered":  good[:len(good)-2] + "xx",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := h.ValidateToken(token); err == nil {
				t.Fatal("token accepted")
			}
		})
	}
}

func TestValidateRequest(t *testing.T) {
	h := New("secret")
	token, err := h.GenerateJWT(DefaultClaims(OptionId("x")))
	if err != nil {
		t.Fatal(err)
	}

	r := httptest.NewRequest("GET", "/", nil)
	if _, err := h.Validate(r); err == nil {
		t.Fatal("request without Authorization accepted")
	}

	r.Header.Set("Authorization", token)
	claims, err := h.Validate(r)
	if err != nil {
		t.Fatalf("validate: %s", err)
	}
	if claims.Id != "x" {
		t.Fatalf("id = %q, want x", claims.Id)
	}
}
