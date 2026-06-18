// Command server is the entry point for the cfgsync backend.
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/viccom/cfgsync/internal/config"
	"github.com/viccom/cfgsync/internal/db"
	"github.com/viccom/cfgsync/internal/repo"
	"github.com/viccom/cfgsync/internal/server"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)

	// First-run convenience: if no cfgsync.env sits next to the binary (or at
	// CFGSYNC_CONFIG), generate one with a random JWT_SECRET and a random
	// bootstrap admin password, then continue. The user gets a working setup
	// by double-clicking the exe, no manual env configuration required.
	cfgFile := os.Getenv("CFGSYNC_CONFIG")
	if cfgFile == "" {
		cfgFile = "cfgsync.env"
	}
	if _, err := os.Stat(cfgFile); os.IsNotExist(err) {
		pw, gerr := config.GenerateDefaultConfig(cfgFile)
		if gerr != nil {
			log.Fatalf("generate default config: %v", gerr)
		}
		log.Printf("first run: generated %s", cfgFile)
		log.Printf("first run: bootstrap admin email=admin@example.com password=%s", pw)
		log.Printf("first run: change the password by editing %s and deleting the DB, or via SQL UPDATE users", cfgFile)
	}

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	database, err := db.Open(cfg.DBPath)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer database.Close()

	if err := db.Migrate(database); err != nil {
		log.Fatalf("migrate: %v", err)
	}

	if err := db.BootstrapAdmin(database, cfg); err != nil {
		log.Fatalf("bootstrap admin: %v", err)
	}
	if cfg.BootstrapAdminEmail != "" {
		log.Printf("bootstrap admin ensured: %s", cfg.BootstrapAdminEmail)
	}

	repository, err := repo.New(cfg.RepoDir)
	if err != nil {
		log.Fatalf("open repo: %v", err)
	}

	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           server.New(cfg, database, repository),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		log.Printf("cfgsync listening on=%s db=%s", cfg.Listen, cfg.DBPath)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	select {
	case sig := <-sigCh:
		log.Printf("received %s, shutting down", sig)
	case err := <-errCh:
		log.Fatalf("server: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Printf("shutdown: %v", err)
	}
}
