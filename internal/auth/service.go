package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
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

func (s *Service) NewLoginURL() (string, *store.PendingOAuth, error) {
	verifier := oauth2.GenerateVerifier()
	state, err := randomURLSafe(24)
	if err != nil {
		return "", nil, err
	}
	url := s.oauthConfig.AuthCodeURL(
		state,
		oauth2.AccessTypeOffline,
		oauth2.S256ChallengeOption(verifier),
		oauth2.SetAuthURLParam("codex_cli_simplified_flow", "true"),
		oauth2.SetAuthURLParam("originator", s.cfg.Originator),
	)
	log.Printf("auth: generated login URL state=%s redirect_uri=%s", state, s.cfg.RedirectURI)
	return url, &store.PendingOAuth{Verifier: verifier, State: state}, nil
}

func (s *Service) ExchangeCode(code, verifier string) (*store.Tokens, error) {
	log.Printf("auth: exchanging authorization code")
	ctx := context.WithValue(context.Background(), oauth2.HTTPClient, s.httpClient)
	token, err := s.oauthConfig.Exchange(ctx, code, oauth2.VerifierOption(verifier))
	if err != nil {
		log.Printf("auth: code exchange failed: %v", err)
		return nil, err
	}
	tokens, err := tokensFromOAuthToken(token)
	if err != nil {
		log.Printf("auth: token parsing failed after code exchange: %v", err)
		return nil, err
	}
	log.Printf("auth: code exchange succeeded account_id=%s expires_at=%d", tokens.AccountID, tokens.ExpiresAt)
	return tokens, nil
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
	log.Printf("auth: refresh succeeded account_id=%s expires_at=%d", tokens.AccountID, tokens.ExpiresAt)
	return tokens, nil
}

func tokensFromOAuthToken(token *oauth2.Token) (*store.Tokens, error) {
	if token == nil {
		return nil, errors.New("missing oauth token")
	}
	accountID, err := extractAccountID(token.AccessToken)
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
	return &store.Tokens{
		AccessToken:  token.AccessToken,
		RefreshToken: refreshToken,
		ExpiresAt:    token.Expiry.Unix(),
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

func randomURLSafe(size int) (string, error) {
	buf := make([]byte, size)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
