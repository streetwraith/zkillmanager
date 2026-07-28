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
	"net"
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
	healthcheck := flag.Bool("healthcheck", false, "probe the local /healthz and exit 0 (healthy) or 1; for the container HEALTHCHECK")
	flag.Parse()

	// Handle -healthcheck before any config load: the container HEALTHCHECK runs this and it
	// must not depend on DATABASE_URL/USER_AGENT being parseable.
	if *healthcheck {
		os.Exit(healthProbe())
	}

	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	if err := run(context.Background(), *backfill); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

// healthProbe backs the container HEALTHCHECK. The distroless image has no shell or curl, so
// the binary probes its own /healthz. HTTP_ADDR is read straight from the env (not via
// config.Load, which requires DATABASE_URL/USER_AGENT), and only its port is used -- we always
// dial 127.0.0.1 so a bind address like ":8080" or "0.0.0.0:8080" still resolves. Returns a
// process exit code: 0 on HTTP 200, 1 on anything else.
func healthProbe() int {
	addr := os.Getenv("HTTP_ADDR")
	if addr == "" {
		addr = ":8080" // keep in sync with config.Load's default
	}
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "healthcheck: invalid HTTP_ADDR %q: %v\n", addr, err)
		return 1
	}
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get("http://127.0.0.1:" + port + "/healthz")
	if err != nil {
		fmt.Fprintf(os.Stderr, "healthcheck: %v\n", err)
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "healthcheck: /healthz returned %s\n", resp.Status)
		return 1
	}
	return 0
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
	syncer := ingest.NewSyncer(hist, hist, ingester, st, st, cfg.RetentionDays, slog.Default())

	// Internal scheduler: hourly reconciliation (picks up new/grown dumps), pod pairing, and
	// retention prune. Pairing goes through the syncer so it never overlaps a bulk day insert.
	sch := scheduler.New(cfg, syncer, syncer, st, slog.Default())
	g.Go(func() error { return sch.Run(ctx) })

	if backfill || cfg.BackfillOnStart {
		g.Go(func() error {
			// A backfill failure must not take the daemon down: the live feed keeps running
			// and the hourly reconcile fills the same gaps. Log and continue (return nil).
			n, err := syncer.Sync(ctx, 0) // limit 0 = all gap days
			switch {
			case err == nil:
				slog.Info("backfill complete", "days", n)
				// Pairing is deferred for the whole backfill (it shares the Sync lock), so a
				// large backlog piles up; drain it now instead of waiting for the next tick.
				switch pods, perr := syncer.PairPods(ctx); {
				case perr == nil && pods > 0:
					slog.Info("post-backfill pod pairing", "deleted", pods)
				case perr != nil && !errors.Is(perr, context.Canceled):
					slog.Error("post-backfill pod pairing failed; scheduler will retry", "err", perr)
				}
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
