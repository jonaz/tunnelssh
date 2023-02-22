package jwt

import (
	"fmt"
	"net/http"
	"time"

	"github.com/golang-jwt/jwt/v4"
)

type JWTHandler struct {
	key []byte
}

func New(key string) *JWTHandler {
	return &JWTHandler{
		key: []byte(key),
	}
}

type Claims struct {
	// Id is the agent id
	Id string `json:"id"`
	jwt.RegisteredClaims
}

func (j *JWTHandler) ValidateToken(signedToken string) (*Claims, error) {
	token, err := jwt.ParseWithClaims(
		signedToken,
		&Claims{},
		func(token *jwt.Token) (interface{}, error) {
			return []byte(j.key), nil
		},
	)
	if err != nil {
		return nil, fmt.Errorf("auth: error validating: %w", err)
	}

	if !token.Valid {
		return nil, fmt.Errorf("token not valid")
	}

	claims, ok := token.Claims.(*Claims)
	if !ok {
		return nil, fmt.Errorf("couldn't parse claims: ")
	}

	return claims, nil
}

func (j *JWTHandler) GenerateJWT(claims *Claims) (tokenString string, err error) {
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tokenString, err = token.SignedString(j.key)
	return
}

func (j *JWTHandler) Validate(r *http.Request) (*Claims, error) {
	tokenString := r.Header.Get("Authorization")
	claims, err := j.ValidateToken(tokenString)
	if err != nil {
		return nil, err
	}

	return claims, nil
}

func DefaultClaims(opts ...OptionsFunc) *Claims {
	expirationTime := time.Now().Add(24 * time.Hour) // TODO how long?
	claim := &Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(expirationTime),
		},
	}

	for _, o := range opts {
		o(claim)
	}
	return claim
}

type OptionsFunc func(*Claims)

func OptionId(id string) OptionsFunc {
	return func(claim *Claims) {
		claim.Id = id
	}
}
