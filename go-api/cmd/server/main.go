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
		telegramService = telegramService.WithUserResolver(userDBService.ResolveClientUserID).WithAdminService(adminDBService)
		adminService = adminDBService
		nodeService = nodeapi.NewDBService(cfg, db, userDBService)
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

func dbReadyCheck(db *sql.DB) func(context.Context) error {
	return func(ctx context.Context) error {
		if db == nil {
			return nil
		}
		return db.PingContext(ctx)
	}
}
