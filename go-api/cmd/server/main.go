package main

import (
	"context"
	"database/sql"
	"errors"
	"log"
	"net/http"
	"os/signal"
	"syscall"

	"forest/go-api/internal/admin"
	"forest/go-api/internal/background"
	"forest/go-api/internal/config"
	"forest/go-api/internal/guest"
	httpapi "forest/go-api/internal/http"
	"forest/go-api/internal/nodeapi"
	"forest/go-api/internal/passport"
	"forest/go-api/internal/payment"
	"forest/go-api/internal/platform/postgres"
	"forest/go-api/internal/queue"
	"forest/go-api/internal/session"
	"forest/go-api/internal/telegram"
	usersvc "forest/go-api/internal/user"
)

func main() {
	cfg := config.Load()
	if err := validateServerConfig(cfg); err != nil {
		log.Fatalf("invalid configuration: %v", err)
	}
	runtimeConfig := config.NewRuntimeState(cfg)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	db, err := postgres.Open(ctx, cfg.PostgresDSN)
	if err != nil {
		log.Fatalf("open postgres: %v", err)
	}
	if db != nil {
		defer func() {
			_ = db.Close()
		}()
		// The old per-node host DSL is intentionally not evaluated at runtime
		// anymore.  Upgrade it atomically before any subscription request can be
		// served, then leave a durable marker so restarts are cheap and idempotent.
		if err := postgres.EnsureClientEntrySchema(ctx, db); err != nil {
			log.Fatalf("ensure client entry schema: %v", err)
		}
		report, err := postgres.MigrateLegacyServerHostEntryRules(ctx, db)
		if err != nil {
			var migrationErr *postgres.LegacyEntryHostMigrationError
			if errors.As(err, &migrationErr) {
				for _, issue := range migrationErr.Issues {
					log.Printf("legacy client entry migration issue: source=%s table=%s type=%s server_id=%d policy_id=%d email=%q host=%q reason=%s", issue.Source, issue.Table, issue.ServerType, issue.ServerID, issue.PolicyID, issue.Email, issue.Host, issue.Reason)
				}
			}
			log.Fatalf("migrate legacy client entry rules: %v", err)
		}
		if !report.AlreadyApplied {
			log.Printf("migrated legacy client entry rules: servers=%d rewritten=%d rules=%d hidden=%d users=%d", report.ServersScanned, report.ServersRewritten, report.RulesCreated, report.HideRulesCreated, report.LegacyEmailsMapped)
		}
		if restored, err := postgres.RestoreLegacyEmailPolicyConditions(ctx, db); err != nil {
			log.Printf("restore legacy email entry conditions (will retry on next start): %v", err)
		} else if restored > 0 {
			log.Printf("restored legacy email entry conditions: policies=%d", restored)
		}
		groupingReport, err := postgres.ConsolidateLegacyServerHostEntryRules(ctx, db)
		if err != nil {
			// This is a presentation/data-compaction follow-up after the critical
			// v1 conversion has committed.  Never take the whole service down if
			// it needs to retry (for example during a transient DB reconnect).
			log.Printf("consolidate legacy client entry rules (will retry on next start): %v", err)
		} else if !groupingReport.AlreadyApplied {
			log.Printf("consolidated legacy client entry rules: merged=%d members=%d", groupingReport.RulesMerged, groupingReport.MembersConsolidated)
		}
	}

	var passportService passport.Service
	var sessionService session.Service
	var userService usersvc.Service
	var paymentService payment.Service
	var adminService admin.Service
	var nodeService nodeapi.Service
	var telegramService *telegram.Service
	var backgroundRunner *background.Runner
	authCache := session.NewAuthCache(session.DefaultAuthCacheTTL)
	jobQueue := queue.NewRuntime(cfg.QueueWorkers, 0)
	jobQueue.Start()
	defer jobQueue.Shutdown(context.Background())
	if db != nil {
		userDBService := usersvc.NewDBService(cfg, db).WithRuntimeConfig(runtimeConfig).WithQueueRuntime(jobQueue).WithAuthCache(authCache)
		telegramService = telegram.NewService(cfg, db).WithRuntimeConfig(runtimeConfig).WithQueueRuntime(jobQueue)
		userDBService = userDBService.WithAdminNotifier(telegramService)
		passportService = passport.NewDBServiceWithConfig(cfg, db).WithRuntimeConfig(runtimeConfig).WithQueueRuntime(jobQueue).WithAuthCache(authCache)
		sessionService = session.NewDBService(cfg, db).WithAuthCache(authCache)
		userService = userDBService
		paymentService = payment.NewDBService(cfg, db, userDBService).WithRuntimeConfig(runtimeConfig)
		adminDBService := admin.NewDBService(cfg, db, userDBService).WithRuntimeConfig(runtimeConfig).WithQueueRuntime(jobQueue).WithAuthCache(authCache)
		if err := initializeDNSFailoverBeforeServe(ctx, adminDBService); err != nil {
			log.Fatalf("initialize DNS failover schema: %v", err)
		}
		adminDBService.WithDNSFailoverNotifier(dnsFailoverNotifierForTelegram(telegramService)).WithDNSFailoverEvaluationRequester(adminDBService)
		startDNSFailoverAutomationAfterSchema(ctx, adminDBService)
		defer func() {
			stopCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
			defer cancel()
			if err := stopDNSFailoverAutomationBeforeDependencies(stopCtx, adminDBService); err != nil {
				log.Printf("stop DNS failover automation: %v", err)
			}
		}()
		telegramService = telegramService.WithUserResolver(userDBService.ResolveClientUserID).WithAdminService(adminDBService)
		adminService = adminDBService
		nodeService = nodeapi.NewDBService(cfg, db, userDBService).WithRuntimeConfig(runtimeConfig)
		backgroundRunner = background.NewRunner(jobQueue, adminDBService, userDBService, adminDBService, adminDBService)
	}

	server := &http.Server{
		Addr: cfg.Addr,
		Handler: httpapi.NewRouter(
			cfg,
			httpapi.WithRuntimeConfig(runtimeConfig),
			httpapi.WithReadyCheck(dbReadyCheck(db)),
			httpapi.WithGuestService(guest.NewDBService(cfg, db).WithRuntimeConfig(runtimeConfig)),
			httpapi.WithPassportService(passportService),
			httpapi.WithSessionService(sessionService),
			httpapi.WithUserService(userService),
			httpapi.WithPaymentService(paymentService),
			httpapi.WithAdminService(adminService),
			httpapi.WithNodeService(nodeService),
			httpapi.WithQueueRuntime(jobQueue),
			httpapi.WithTelegramService(telegramService),
		),
		ReadTimeout:  cfg.ReadTimeout,
		WriteTimeout: cfg.WriteTimeout,
	}

	go func() {
		<-ctx.Done()

		shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
		defer cancel()

		if err := server.Shutdown(shutdownCtx); err != nil {
			log.Printf("server shutdown error: %v", err)
		}
	}()

	if backgroundRunner != nil {
		backgroundRunner.Start(ctx)
	}

	log.Printf("go api listening on %s", cfg.Addr)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("listen and serve: %v", err)
	}
}

func validateServerConfig(cfg config.Config) error {
	return config.ValidateProbeTrustedProxyCIDRs(cfg.ProbeTrustedProxyCIDRs)
}

type dnsFailoverSchemaInitializer interface {
	InitializeDNSFailoverSchema(context.Context) error
}

type dnsFailoverAutomationStarter interface {
	StartDNSFailoverAutomation(context.Context)
}

type dnsFailoverAutomationStopper interface {
	StopDNSFailoverAutomation(context.Context) error
}

func initializeDNSFailoverBeforeServe(ctx context.Context, initializer dnsFailoverSchemaInitializer) error {
	if initializer == nil {
		return nil
	}
	return initializer.InitializeDNSFailoverSchema(ctx)
}

func startDNSFailoverAutomationAfterSchema(ctx context.Context, starter dnsFailoverAutomationStarter) {
	if starter != nil {
		starter.StartDNSFailoverAutomation(ctx)
	}
}

func stopDNSFailoverAutomationBeforeDependencies(ctx context.Context, stopper dnsFailoverAutomationStopper) error {
	if stopper == nil {
		return nil
	}
	return stopper.StopDNSFailoverAutomation(ctx)
}

func dnsFailoverNotifierForTelegram(service *telegram.Service) admin.DNSFailoverNotifier {
	if service == nil {
		return nil
	}
	return service.DirectNotifier()
}

func dbReadyCheck(db *sql.DB) func(context.Context) error {
	return func(ctx context.Context) error {
		if db == nil {
			return nil
		}
		return db.PingContext(ctx)
	}
}
