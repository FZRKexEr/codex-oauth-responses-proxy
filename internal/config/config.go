package config

import (
	"os"
	"strings"
	"time"

	"golang.org/x/oauth2"
)

type Config struct {
	ListenAddr       string
	ClientID         string
	AuthURL          string
	TokenURL         string
	RedirectURI      string
	Scopes           string
	Originator       string
	BetaHeader       string
	BackendBase      string
	TokenFile        string
	RequestTimeout   time.Duration
	RefreshBuffer    time.Duration
	DebugRequestBody bool
}

func Load() Config {
	return Config{
		ListenAddr:       env("LISTEN_ADDR", "127.0.0.1:1455"),
		ClientID:         env("OPENAI_OAUTH_CLIENT_ID", "app_EMoamEEZ73f0CkXaXp7hrann"),
		AuthURL:          env("OPENAI_OAUTH_AUTH_URL", "https://auth.openai.com/oauth/authorize"),
		TokenURL:         env("OPENAI_OAUTH_TOKEN_URL", "https://auth.openai.com/oauth/token"),
		RedirectURI:      env("OPENAI_OAUTH_REDIRECT_URI", "http://localhost:1455/auth/callback"),
		Scopes:           env("OPENAI_OAUTH_SCOPES", "openid profile email offline_access"),
		Originator:       env("OPENAI_OAUTH_ORIGINATOR", "codex_cli_rs"),
		BetaHeader:       env("OPENAI_OAUTH_BETA", "responses=experimental"),
		BackendBase:      env("OPENAI_BACKEND_BASE", "https://chatgpt.com/backend-api"),
		TokenFile:        env("OPENAI_OAUTH_TOKEN_FILE", ".oauth_tokens.json"),
		RequestTimeout:   durationSeconds("OPENAI_PROXY_TIMEOUT", 180),
		RefreshBuffer:    durationSeconds("OPENAI_OAUTH_REFRESH_BUFFER_SECONDS", 300),
		DebugRequestBody: envBool("DEBUG_REQUEST_BODY", false),
	}
}

func (c Config) OAuthConfig() *oauth2.Config {
	return &oauth2.Config{
		ClientID: c.ClientID,
		Endpoint: oauth2.Endpoint{
			AuthURL:  c.AuthURL,
			TokenURL: c.TokenURL,
		},
		RedirectURL: c.RedirectURI,
		Scopes:      strings.Fields(c.Scopes),
	}
}

func env(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func durationSeconds(key string, fallback int) time.Duration {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return time.Duration(fallback) * time.Second
	}
	parsed, err := time.ParseDuration(value + "s")
	if err != nil {
		return time.Duration(fallback) * time.Second
	}
	return parsed
}

func envBool(key string, fallback bool) bool {
	value := strings.TrimSpace(strings.ToLower(os.Getenv(key)))
	if value == "" {
		return fallback
	}
	switch value {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return fallback
	}
}
