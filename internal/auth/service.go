package auth

import (
	"context"
	"errors"
	"log"
	"net/http"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/oauth2"

	"oauth-responses-proxy/internal/config"
	"oauth-responses-proxy/internal/store"
)

type Service struct {
	cfg         config.Config
	httpClient  *http.Client
	oauthConfig *oauth2.Config
}

func NewService(cfg config.Config, httpClient *http.Client) *Service {
	return &Service{
		cfg:         cfg,
		httpClient:  httpClient,
		oauthConfig: cfg.OAuthConfig(),
	}
}

func (s *Service) RefreshTokens(refreshToken string) (*store.Tokens, error) {
	log.Printf("auth: refreshing access token")
	ctx := context.WithValue(context.Background(), oauth2.HTTPClient, s.httpClient)
	source := s.oauthConfig.TokenSource(ctx, &oauth2.Token{
		RefreshToken: refreshToken,
	})
	token, err := source.Token()
	if err != nil {
		log.Printf("auth: refresh failed: %v", err)
		return nil, err
	}
	tokens, err := tokensFromOAuthToken(token)
	if err != nil {
		log.Printf("auth: token parsing failed after refresh: %v", err)
		return nil, err
	}
	if tokens.RefreshToken == "" {
		tokens.RefreshToken = refreshToken
	}
	log.Printf("auth: refresh succeeded account_id=%s expires_at=%d", tokens.AccountID, tokens.ExpiresAt)
	return tokens, nil
}

func tokensFromOAuthToken(token *oauth2.Token) (*store.Tokens, error) {
	if token == nil {
		return nil, errors.New("missing oauth token")
	}
	idToken, _ := token.Extra("id_token").(string)
	accountID, err := extractAccountID(token.AccessToken)
	if err != nil && idToken != "" {
		accountID, err = extractAccountID(idToken)
	}
	if err != nil {
		return nil, err
	}
	refreshToken := token.RefreshToken
	if refreshToken == "" {
		if raw := token.Extra("refresh_token"); raw != nil {
			if value, ok := raw.(string); ok {
				refreshToken = value
			}
		}
	}
	expiresAt := token.Expiry.Unix()
	if token.Expiry.IsZero() {
		expiresAt = extractExpiry(token.AccessToken)
		if expiresAt == 0 && idToken != "" {
			expiresAt = extractExpiry(idToken)
		}
	}
	return &store.Tokens{
		AccessToken:  token.AccessToken,
		RefreshToken: refreshToken,
		ExpiresAt:    expiresAt,
		AccountID:    accountID,
	}, nil
}

func extractAccountID(accessToken string) (string, error) {
	parsed, _, err := new(jwt.Parser).ParseUnverified(accessToken, jwt.MapClaims{})
	if err != nil {
		return "", err
	}
	claims, ok := parsed.Claims.(jwt.MapClaims)
	if !ok {
		return "", errors.New("invalid token claims")
	}
	authClaim, ok := claims["https://api.openai.com/auth"].(map[string]any)
	if ok {
		if accountID, ok := authClaim["chatgpt_account_id"].(string); ok && accountID != "" {
			return accountID, nil
		}
	}
	if sub, ok := claims["sub"].(string); ok && sub != "" {
		return sub, nil
	}
	return "", errors.New("could not determine account ID from access token")
}

func extractExpiry(token string) int64 {
	parsed, _, err := new(jwt.Parser).ParseUnverified(token, jwt.MapClaims{})
	if err != nil {
		return 0
	}
	claims, ok := parsed.Claims.(jwt.MapClaims)
	if !ok {
		return 0
	}
	exp, err := claims.GetExpirationTime()
	if err != nil || exp == nil {
		return 0
	}
	return exp.Time.Unix()
}
