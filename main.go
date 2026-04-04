package main

import (
	"log"
	"net/http"

	"oauth-responses-proxy/internal/auth"
	"oauth-responses-proxy/internal/config"
	"oauth-responses-proxy/internal/httpapi"
	"oauth-responses-proxy/internal/proxy"
	"oauth-responses-proxy/internal/store"
)

func main() {
	cfg := config.Load()
	tokenStore := store.NewTokenStore(cfg.TokenFile)
	httpClient := &http.Client{Timeout: cfg.RequestTimeout}
	authService := auth.NewService(cfg, httpClient)
	proxyService := proxy.NewService(cfg, tokenStore, httpClient, authService)
	handler := httpapi.NewHandler(cfg, tokenStore, authService, proxyService)

	log.Printf("listening on http://%s", cfg.ListenAddr)
	log.Fatal(http.ListenAndServe(cfg.ListenAddr, handler.Routes()))
}
