package main

import (
	"context"
	"database/sql"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"

	apiRCS "github.com/zagnu-labs/rcs-messenger/internal/api/rcs"
	"github.com/zagnu-labs/rcs-messenger/internal/db"
	"github.com/zagnu-labs/rcs-messenger/internal/logg"
	"github.com/zagnu-labs/rcs-messenger/internal/providers/rcsphone"
	"github.com/zagnu-labs/rcs-messenger/internal/rcs"
)

const maxSendWait = 20 * time.Second

type config struct {
	Addr           string
	DatabaseURL    string
	Debug          bool
	Enabled        bool
	RequireE2EE    bool
	DryRun         bool
	WaitSeconds    int
	MonitorSeconds int
	Allowlist      []string
}

func loadConfig() config {
	return config{
		Addr:           env("ADDR", "127.0.0.1:8080"),
		DatabaseURL:    env("DATABASE_URL", "postgres://postgres:postgres@localhost:5432/rcs_messenger?sslmode=disable"),
		Debug:          envBool("DEBUG", false),
		Enabled:        envBool("RCS_ENABLED", true),
		RequireE2EE:    envBool("RCS_REQUIRE_E2EE", false),
		DryRun:         envBool("RCS_DRY_RUN", false),
		WaitSeconds:    envInt("RCS_WAIT_SECONDS", 20),
		MonitorSeconds: envInt("RCS_MONITOR_SECONDS", 60),
		Allowlist:      envList("RCS_ALLOWLIST_PHONES"),
	}
}

func main() {
	cfg := loadConfig()
	logg.Setup(cfg.Debug)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	conn, err := db.Open(cfg.DatabaseURL)
	if err != nil {
		logg.Fatal().Err(err).Msg("Failed to open the database")
	}
	defer conn.Close()

	stack := setupRCS(cfg, conn)
	if cfg.Enabled {
		go stack.runner.Start(ctx, nil)
		go rcs.NewMonitor(stack.service, nil, time.Duration(cfg.MonitorSeconds)*time.Second).Start(ctx, nil)
	}

	srv := setupRouter(cfg, stack)
	go func() {
		logg.Info().Str("addr", cfg.Addr).Msg("Starting HTTP server")
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logg.Fatal().Err(err).Msg("Failed to start HTTP server")
		}
	}()

	<-ctx.Done()
	logg.Info().Msg("Shutting down...")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logg.Error().Err(err).Msg("HTTP server forced to shutdown")
	}
}

type rcsStack struct {
	service *rcs.Service
	runner  *rcsphone.Runner
	pairing *rcsphone.Pairing
}

func setupRCS(cfg config, conn *sql.DB) *rcsStack {
	store := rcsphone.NewStore(conn)
	sender := rcsphone.NewSender()
	runner := rcsphone.NewRunner(store, sender, logg.Logger())
	pairing := rcsphone.NewPairing(store, runner, logg.Logger())

	wait := time.Duration(cfg.WaitSeconds) * time.Second
	if wait <= 0 {
		wait = 20 * time.Second
	}
	if wait > maxSendWait {
		logg.Warn().
			Dur("configured", wait).
			Dur("capped", maxSendWait).
			Msg("RCS_WAIT_SECONDS exceeds the HTTP write timeout, capping")
		wait = maxSendWait
	}

	svc := rcs.NewService(sender, rcs.Config{
		Enabled:     cfg.Enabled,
		RequireE2EE: cfg.RequireE2EE,
		Wait:        wait,
		DryRun:      cfg.DryRun,
		Allowlist:   cfg.Allowlist,
	}).SetResults(rcs.NewResultStore(conn))

	runner.OnInbound(func(from, text, messageID string) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if _, err := svc.HandleInbound(ctx, rcs.Inbound{From: from, Text: text, MessageID: messageID}); err != nil {
			logg.Error().Err(err).Str("from", from).Msg("RCS inbound could not be handled")
		}
	})

	switch {
	case !cfg.Enabled:
		logg.Info().Msg("RCS sending disabled")
	case cfg.DryRun:
		logg.Info().Msg("RCS enabled in dry-run, nothing reaches the phone")
	default:
		logg.Info().
			Bool("require_e2ee", cfg.RequireE2EE).
			Int("allowlisted", len(cfg.Allowlist)).
			Msg("RCS sending enabled")
	}
	return &rcsStack{service: svc, runner: runner, pairing: pairing}
}

func setupRouter(cfg config, stack *rcsStack) *http.Server {
	if !cfg.Debug {
		gin.SetMode(gin.ReleaseMode)
	}
	engine := gin.New()
	engine.Use(gin.Recovery())

	e := apiRCS.NewEndpoint(stack.service).SetPairing(stack.pairing)
	r := engine.Group("/internal/rcs")

	r.GET("/health", e.Health)
	r.POST("/check", e.Check)
	r.POST("/send", e.Send)
	r.GET("/thread", e.Thread)
	r.POST("/inbound", e.Inbound)
	r.POST("/nudge", e.Nudge)
	r.GET("/results", e.Results)

	r.GET("/pair", e.PairPage)
	r.POST("/pair/start", e.PairStart)
	r.GET("/pair/qr", e.PairQR)
	r.GET("/pair/status", e.PairStatus)
	r.POST("/pair/cancel", e.PairCancel)
	r.POST("/pair/cookies", e.PairCookies)
	r.POST("/pair/refresh", e.PairRefresh)

	return &http.Server{
		Addr:         cfg.Addr,
		Handler:      engine,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}
}

func env(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func envBool(key string, fallback bool) bool {
	if v, err := strconv.ParseBool(env(key, "")); err == nil {
		return v
	}
	return fallback
}

func envInt(key string, fallback int) int {
	if v, err := strconv.Atoi(env(key, "")); err == nil {
		return v
	}
	return fallback
}

func envList(key string) []string {
	var out []string
	for _, part := range strings.Split(env(key, ""), ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}
