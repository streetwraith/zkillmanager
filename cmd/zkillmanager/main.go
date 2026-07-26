// Command zkillmanager is the collector: a single long-running service that tails the
// R2Z2 live feed, runs an internal cron (daily dump + prune), backfills on demand, and
// serves an internal /healthz.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/joho/godotenv"
	"golang.org/x/sync/errgroup"

	"zkillmanager/internal/config"
	"zkillmanager/internal/enrich"
	"zkillmanager/internal/ingest"
	"zkillmanager/internal/scheduler"
	"zkillmanager/internal/store"
	"zkillmanager/internal/zkill"
)

func main() {
	backfill := flag.Bool("backfill", false, "run a one-shot backfill on start")
	flag.Parse()

	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	if err := run(context.Background(), *backfill); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, backfill bool) error {
	loadEnvFile()

	cfg, err := config.Load()
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	st, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer st.Close()

	if err := st.Migrate(ctx); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}

	// Load the SDE system->space lookup once (static data); fail fast if sde is missing.
	records, err := st.LoadSystemRecords(ctx)
	if err != nil {
		return fmt.Errorf("load sde systems: %w", err)
	}
	resolver := enrich.NewResolver(records)
	slog.Info("resolver loaded", "systems", resolver.Len())

	// Materialize the stealth-ship table: seed groups expanded via the SDE, unioned with
	// the explicit hulls. Readers (lscan) join it; ingest no longer carries the flag.
	stealthGroupTypes, err := st.LoadStealthTypeIDs(ctx, enrich.StealthGroupIDs)
	if err != nil {
		return fmt.Errorf("load stealth types: %w", err)
	}
	nStealth, err := st.SyncStealthShips(ctx, append(stealthGroupTypes, enrich.StealthTypeIDs...))
	if err != nil {
		return fmt.Errorf("sync stealth ships: %w", err)
	}
	slog.Info("stealth ships synced", "count", nStealth)

	ingester := ingest.New(resolver, st, slog.Default())

	g, ctx := errgroup.WithContext(ctx)

	srv := &http.Server{
		Addr:         cfg.HTTPAddr,
		Handler:      newMux(st),
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  60 * time.Second,
	}
	g.Go(func() error {
		slog.Info("http listening", "addr", cfg.HTTPAddr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	})
	g.Go(func() error {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	})

	// Live tail (R2Z2 ephemeral feed; resumes via ingest_state).
	live := zkill.NewLive(cfg.LiveBaseURL, cfg.UserAgent, ingester, st)
	g.Go(func() error { return live.Run(ctx) })

	// One Syncer, shared by the scheduler's reconciliation and the optional backfill
	// (its TryLock keeps them from overlapping).
	hist := zkill.NewHistory(cfg.HistoryBaseURL, cfg.UserAgent)
	syncer := ingest.NewSyncer(hist, hist, ingester, st, cfg.RetentionDays, slog.Default())

	// Internal scheduler: hourly reconciliation (picks up new/grown dumps) + retention prune.
	sch := scheduler.New(cfg, syncer, st, slog.Default())
	g.Go(func() error { return sch.Run(ctx) })

	if backfill || cfg.BackfillOnStart {
		g.Go(func() error {
			// A backfill failure must not take the daemon down: the live feed keeps running
			// and the hourly reconcile fills the same gaps. Log and continue (return nil).
			n, err := syncer.Sync(ctx, 0) // limit 0 = all gap days
			switch {
			case err == nil:
				slog.Info("backfill complete", "days", n)
			case errors.Is(err, context.Canceled):
				// shutdown, not a failure
			default:
				slog.Error("backfill failed; live feed + scheduler continue, reconcile will catch up", "err", err)
			}
			return nil
		})
	}

	if err := g.Wait(); err != nil {
		return err
	}
	slog.Info("shutdown complete")
	return nil
}

// loadEnvFile loads a local .env for dev convenience if one is present. Prod (Coolify)
// injects real env vars and ships no .env, so this is a no-op there; existing environment
// variables are never overridden.
func loadEnvFile() {
	if _, err := os.Stat(".env"); err != nil {
		return
	}
	if err := godotenv.Load(); err != nil {
		slog.Warn("failed to load .env", "err", err)
		return
	}
	slog.Info("loaded .env")
}

func newMux(st *store.Store) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := st.Ping(ctx); err != nil {
			http.Error(w, "db unavailable", http.StatusServiceUnavailable)
			return
		}
		_, _ = io.WriteString(w, "ok")
	})
	return mux
}
