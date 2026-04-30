package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"golang.org/x/oauth2"

	"oauth-responses-proxy/internal/store"
)

const deviceAuthMaxWait = 15 * time.Minute

type DeviceCode struct {
	VerificationURL string
	UserCode        string
	Interval        int64
	ExpiresAt       int64
}

type deviceUserCodeResponse struct {
	DeviceAuthID string         `json:"device_auth_id"`
	UserCode     string         `json:"user_code"`
	Interval     deviceInterval `json:"interval"`
}

type deviceTokenResponse struct {
	AuthorizationCode string `json:"authorization_code"`
	CodeVerifier      string `json:"code_verifier"`
}

type deviceInterval int64

func (i *deviceInterval) UnmarshalJSON(data []byte) error {
	var asNumber int64
	if err := json.Unmarshal(data, &asNumber); err == nil {
		*i = deviceInterval(asNumber)
		return nil
	}
	var asString string
	if err := json.Unmarshal(data, &asString); err != nil {
		return err
	}
	parsed, err := strconv.ParseInt(strings.TrimSpace(asString), 10, 64)
	if err != nil {
		return err
	}
	*i = deviceInterval(parsed)
	return nil
}

func (s *Service) NewDeviceCode(ctx context.Context) (*DeviceCode, *store.PendingDeviceAuth, error) {
	issuer, err := s.oauthIssuer()
	if err != nil {
		return nil, nil, err
	}
	apiBase := issuer + "/api/accounts"
	body, err := json.Marshal(map[string]string{"client_id": s.cfg.ClientID})
	if err != nil {
		return nil, nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiBase+"/deviceauth/usercode", bytes.NewReader(body))
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, nil, errors.New("device code login is not enabled for this Codex server")
	}
	if resp.StatusCode >= 400 {
		return nil, nil, responseError("device code request failed", resp)
	}
	var result deviceUserCodeResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, nil, err
	}
	if strings.TrimSpace(result.DeviceAuthID) == "" || strings.TrimSpace(result.UserCode) == "" {
		return nil, nil, errors.New("device code response missing required fields")
	}
	interval := int64(result.Interval)
	if interval <= 0 {
		interval = 5
	}
	expiresAt := time.Now().Add(deviceAuthMaxWait).Unix()
	verificationURL := issuer + "/codex/device"
	code := &DeviceCode{
		VerificationURL: verificationURL,
		UserCode:        result.UserCode,
		Interval:        interval,
		ExpiresAt:       expiresAt,
	}
	pending := &store.PendingDeviceAuth{
		DeviceAuthID:    result.DeviceAuthID,
		UserCode:        result.UserCode,
		VerificationURL: verificationURL,
		Interval:        interval,
		ExpiresAt:       expiresAt,
	}
	return code, pending, nil
}

func (s *Service) ExchangeDeviceCode(ctx context.Context, pending *store.PendingDeviceAuth) (*store.Tokens, error) {
	if pending == nil {
		return nil, errors.New("missing pending device auth")
	}
	if strings.TrimSpace(pending.DeviceAuthID) == "" || strings.TrimSpace(pending.UserCode) == "" {
		return nil, errors.New("invalid pending device auth")
	}
	issuer, err := s.oauthIssuer()
	if err != nil {
		return nil, err
	}
	codeResp, err := s.pollDeviceCode(ctx, issuer, pending)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(codeResp.AuthorizationCode) == "" || strings.TrimSpace(codeResp.CodeVerifier) == "" {
		return nil, errors.New("device auth token response missing required fields")
	}
	oauthConfig := *s.oauthConfig
	oauthConfig.RedirectURL = issuer + "/deviceauth/callback"
	ctx = context.WithValue(ctx, oauth2.HTTPClient, s.httpClient)
	token, err := oauthConfig.Exchange(ctx, codeResp.AuthorizationCode, oauth2.VerifierOption(codeResp.CodeVerifier))
	if err != nil {
		return nil, err
	}
	return tokensFromOAuthToken(token)
}

func (s *Service) pollDeviceCode(ctx context.Context, issuer string, pending *store.PendingDeviceAuth) (*deviceTokenResponse, error) {
	apiBase := issuer + "/api/accounts"
	interval := time.Duration(pending.Interval) * time.Second
	if interval <= 0 {
		interval = 5 * time.Second
	}
	deadline := time.Now().Add(deviceAuthMaxWait)
	if pending.ExpiresAt > 0 {
		deadline = time.Unix(pending.ExpiresAt, 0)
	}
	for {
		if time.Now().After(deadline) {
			return nil, errors.New("device auth timed out after 15 minutes")
		}
		body, err := json.Marshal(map[string]string{
			"device_auth_id": pending.DeviceAuthID,
			"user_code":      pending.UserCode,
		})
		if err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiBase+"/deviceauth/token", bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := s.httpClient.Do(req)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			var result deviceTokenResponse
			err := json.NewDecoder(resp.Body).Decode(&result)
			_ = resp.Body.Close()
			if err != nil {
				return nil, err
			}
			return &result, nil
		}
		if resp.StatusCode != http.StatusForbidden && resp.StatusCode != http.StatusNotFound {
			err := responseError("device auth failed", resp)
			_ = resp.Body.Close()
			return nil, err
		}
		_ = resp.Body.Close()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(interval):
		}
	}
}

func (s *Service) oauthIssuer() (string, error) {
	parsed, err := url.Parse(s.cfg.TokenURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", errors.New("could not determine OAuth issuer")
	}
	parsed.RawQuery = ""
	parsed.Fragment = ""
	parsed.Path = strings.TrimSuffix(parsed.Path, "/oauth/token")
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	return parsed.String(), nil
}

func responseError(prefix string, resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	detail := strings.TrimSpace(string(body))
	if detail == "" {
		return fmt.Errorf("%s with status %d", prefix, resp.StatusCode)
	}
	return fmt.Errorf("%s with status %d: %s", prefix, resp.StatusCode, detail)
}
