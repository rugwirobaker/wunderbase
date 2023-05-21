package main

import (
	"context"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"

	"wunderbase/pkg/api"
	"wunderbase/pkg/migrate"
	"wunderbase/pkg/queryengine"

	"github.com/caarlos0/env/v6"
)

type config struct {
	Production            bool   `env:"PRODUCTION" envDefault:"false"`
	PrismaSchemaFilePath  string `env:"PRISMA_SCHEMA_FILE" envDefault:"./schema.prisma"`
	MigrationLockFilePath string `env:"MIGRATION_LOCK_FILE" envDefault:"migration.lock"`
	EnableSleepMode       bool   `env:"ENABLE_SLEEP_MODE" envDefault:"true"`
	SleepAfterSeconds     int    `env:"SLEEP_AFTER_SECONDS" envDefault:"10"`
	// I think that we should discard `EnablePlayground`, when we add `Production` flag.
	// EnablePlayground      bool   `env:"ENABLE_PLAYGROUND" envDefault:"true"`
	MigrationEnginePath string `env:"MIGRATION_ENGINE_PATH" envDefault:"./migration-engine"`
	QueryEnginePath     string `env:"QUERY_ENGINE_PATH" envDefault:"./query-engine"`
	QueryEnginePort     string `env:"QUERY_ENGINE_PORT" envDefault:"4467"`
	ListenAddr          string `env:"LISTEN_ADDR" envDefault:"0.0.0.0:4466"`
	GraphiQLApiURL      string `env:"GRAPHIQL_API_URL" envDefault:"http://localhost:4466"`
	ReadLimitSeconds    int    `env:"READ_LIMIT_SECONDS" envDefault:"10000"`
	WriteLimitSeconds   int    `env:"WRITE_LIMIT_SECONDS" envDefault:"2000"`
	HealthEndpoint      string `env:"HEALTH_ENDPOINT" envDefault:"/health"`
}

func main() {
	if err := Run(context.Background(), os.Args[1:]); err == flag.ErrHelp {
		os.Exit(2)
	} else if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: %s\n", err)
		os.Exit(1)
	}
}

func Run(ctx context.Context, args []string) (err error) {
	var cmd string
	if len(args) > 0 {
		cmd = args[0]
	}

	config := &config{}
	if err := env.Parse(config); err != nil {
		log.Fatalln("parse env", err)
	}

	switch cmd {
	case "migrate":
		return runMigrate(ctx, config)
	case "serve":
		return runServe(ctx, config)
	default:
		if cmd == "" || cmd == "help" || strings.HasPrefix(cmd, "-") {
			printUsage()
			return flag.ErrHelp
		}
		return fmt.Errorf("wunderbase: unknown subcommand %q", cmd)
	}
}

func printUsage() {
	fmt.Println(`
wunderbase is is a Serverless SQLite database exposed through GraphQL. For more information, 
see https://github.com/wundergraph/wunderbase

Usage:
	wunderbase <command> [arguments]

The commands are:
	migrate     Migrate the database schema
	serve       Start the wunderbase server
`[1:])
}

func runMigrate(ctx context.Context, config *config) (err error) {
	schema, err := ioutil.ReadFile(config.PrismaSchemaFilePath)
	if err != nil {
		log.Fatalln("load prisma schema", err)
	}
	migrate.Database(config.MigrationEnginePath, config.MigrationLockFilePath, string(schema), config.PrismaSchemaFilePath)
	return nil
}

func runServe(ctx context.Context, config *config) (err error) {
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	wg := &sync.WaitGroup{}
	wg.Add(2)
	go queryengine.Run(ctx, wg, config.QueryEnginePath, config.QueryEnginePort, config.PrismaSchemaFilePath, config.Production)

	log.Printf("Server Listening on: http://%s", config.ListenAddr)
	handler := api.NewHandler(config.EnableSleepMode,
		config.Production,
		fmt.Sprintf("http://localhost:%s/", config.QueryEnginePort),
		fmt.Sprintf("http://localhost:%s/sdl", config.QueryEnginePort),
		config.HealthEndpoint,
		config.SleepAfterSeconds,
		config.ReadLimitSeconds,
		config.WriteLimitSeconds,
		stop,
	)

	srv := http.Server{
		Addr:    config.ListenAddr,
		Handler: handler,
	}
	go func() {
		err = srv.ListenAndServe()
		if err != nil && err != http.ErrServerClosed {
			log.Fatalln("listen and serve", err)
		}
	}()
	<-ctx.Done()
	err = srv.Close()
	if err != nil {
		log.Fatalln("close server", err)
	}
	log.Println("Server stopped")
	wg.Done()
	wg.Wait()

	return nil
}
