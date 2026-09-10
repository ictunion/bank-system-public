package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kubik/bank-system/internal/config"
	"github.com/kubik/bank-system/internal/db"
	"github.com/kubik/bank-system/internal/handler"
	"github.com/kubik/bank-system/internal/keycloak"
	"github.com/kubik/bank-system/internal/orca"
	"github.com/kubik/bank-system/internal/processing"
	"github.com/kubik/bank-system/internal/scheduler"
	"github.com/kubik/bank-system/internal/syncjob"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatal(err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("db pool: %v", err)
	}
	defer pool.Close()

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		log.Fatalf("db ping: %v", err)
	}

	if fioCount, orcaCount, err := syncjob.FailStaleRuns(ctx, pool); err != nil {
		log.Printf("failing stale sync runs: %v", err)
	} else if fioCount > 0 || orcaCount > 0 {
		log.Printf("failed stale sync runs from a previous process: sync_fio_runs=%d sync_orca_runs=%d", fioCount, orcaCount)
	}

	// One daily job, run in order: Orca (members) before Fio (transactions) —
	// transaction processing (member payment matching, not built yet) needs
	// both done first, so it can't run as two independently-scheduled jobs.
	orcaClient := orca.NewClient(cfg.OrcaAPIURL, cfg.OrcaSyncToken, cfg.Debug)
	go scheduler.RunImmediatellyAndThenDaily(ctx, 3, 0, func(runCtx context.Context) {
		orcaOK := true
		if result, err := syncjob.RunOrcaSync(runCtx, pool, orcaClient); err != nil {
			orcaOK = false
			log.Printf("orca sync: %v", err)
		} else {
			log.Printf("orca sync: members_fetched=%d members_upserted=%d", result.MembersFetched, result.MembersUpserted)
		}

		fioOK := true
		if cfg.DisableFioSync {
			log.Print("fio sync: disabled via DISABLE_FIO_SYNC, skipping")
		} else if results, err := syncjob.RunFioSync(runCtx, pool, cfg.BankTokenEncryptionKey, cfg.Debug); err != nil {
			fioOK = false
			log.Printf("fio sync: %v", err)
		} else {
			for _, r := range results {
				log.Printf("fio sync: bank_account_id=%d fetched=%d inserted=%d", r.BankAccountID, r.TransactionsFetched, r.TransactionsInserted)
			}
		}

		if !orcaOK || !fioOK {
			log.Print("transaction processing: skipped, a sync step failed this run")
			return
		}

		result, err := processing.Run(runCtx, pool)
		if err != nil {
			log.Printf("transaction processing: %v", err)
			return
		}
		log.Printf("transaction processing: processed=%d failed=%d", result.TransactionsProcessed, result.TransactionsFailed)
	})

	keycloakProvider, err := keycloak.NewProvider(cfg.KeycloakHost, cfg.KeycloakRealm, cfg.KeycloakClientID)
	if err != nil {
		log.Fatalf("keycloak: %v", err)
	}

	// All application routes live under /api so a reverse proxy can serve the
	// frontend at / and forward only /api/ here (see docs deploy notes). Handler
	// patterns below stay unprefixed; StripPrefix trims /api before matching.
	api := http.NewServeMux()

	api.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	api.HandleFunc("GET /account", handler.RequireRole(keycloakProvider, keycloak.RoleManageBankAccounts, handler.ListBankAccounts(db.New(pool))))
	api.HandleFunc("POST /account", handler.RequireRole(keycloakProvider, keycloak.RoleManageBankAccounts, handler.CreateBankAccount(db.New(pool), cfg.BankTokenEncryptionKey)))
	api.HandleFunc("PATCH /account/{id}", handler.RequireRole(keycloakProvider, keycloak.RoleManageBankAccounts, handler.UpdateBankAccount(db.New(pool), cfg.BankTokenEncryptionKey)))
	api.HandleFunc("DELETE /account/{id}", handler.RequireRole(keycloakProvider, keycloak.RoleManageBankAccounts, handler.DeleteBankAccount(db.New(pool))))
	api.HandleFunc("POST /account/{id}/sync", handler.RequireRole(keycloakProvider, keycloak.RoleManageBankAccounts, handler.TriggerFioSync(pool, cfg.BankTokenEncryptionKey, cfg.DisableFioSync, cfg.Debug)))

	api.HandleFunc("GET /event-logs", handler.RequireRole(keycloakProvider, keycloak.RoleViewEventLogs, handler.ListEventLogs(db.New(pool))))

	api.HandleFunc("GET /transactions", handler.RequireRole(keycloakProvider, keycloak.RoleListTransactions, handler.ListTransactions(db.New(pool))))
	api.HandleFunc("GET /transactions/{id}", handler.RequireRole(keycloakProvider, keycloak.RoleListTransactions, handler.GetTransaction(db.New(pool))))
	api.HandleFunc("PUT /transactions/{id}/assignment", handler.RequireRole(keycloakProvider, keycloak.RoleManageTransactions, handler.AssignTransaction(pool)))
	api.HandleFunc("DELETE /transactions/{id}/assignment", handler.RequireRole(keycloakProvider, keycloak.RoleManageTransactions, handler.UnassignTransaction(pool)))
	api.HandleFunc("GET /payments/{member_number}/history", handler.RequireRole(keycloakProvider, keycloak.RolePaymentHistory, handler.PaymentHistory(db.New(pool))))
	api.HandleFunc("GET /payments/me/history", handler.RequireAuth(keycloakProvider, handler.MyPaymentHistory(db.New(pool))))
	api.HandleFunc("GET /payments/{year}/{month}/missing", handler.RequireRole(keycloakProvider, keycloak.RolePaymentHistory, handler.MissingPayments(db.New(pool))))
	api.HandleFunc("GET /payments/{year}/missing", handler.RequireRole(keycloakProvider, keycloak.RolePaymentHistory, handler.MissingPaymentsInYear(db.New(pool))))

	mux := http.NewServeMux()
	mux.Handle("/api/", http.StripPrefix("/api", api))

	server := &http.Server{
		Addr:    cfg.Addr,
		Handler: handler.Recover(mux),
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			log.Printf("shutdown: %v", err)
		}
	}()

	log.Printf("listening on %s", cfg.Addr)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}
