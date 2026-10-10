package auth

import (
	"errors"
	"net/http"
	"strings"
)

func GetAPIKey(headers http.Header) (string, error) {
	authHeader := headers.Get("Authorization")
	if authHeader == "" {
		return "", errors.New("missing authorization header")
	}

	if !strings.HasPrefix(authHeader, "ApiKey ") {
		return "", errors.New("invalid authorization header")
	}
	return strings.TrimSpace(strings.TrimPrefix(authHeader, "ApiKey ")), nil
}
