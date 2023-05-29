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
	"golang.org/x/exp/slog"
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
	LogFormat           string `env:"LOG_FORMAT" envDefault:"text"`
	Timestamp           bool   `env:"TIMESTAMP" envDefault:"false"`
	Debug               bool   `env:"DEBUG" envDefault:"true"`
}

var LogLevel struct {
	sync.Mutex
	slog.LevelVar
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
		return fmt.Errorf("wunderbase: parse env: %w", err)
	}

	if err := initLogger(config); err != nil {
		return fmt.Errorf("wunderbase: init logger: %w", err)
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

	err = queryengine.Run(ctx, wg,
		config.QueryEnginePath,
		config.QueryEnginePort,
		config.PrismaSchemaFilePath,
		config.Production,
		config.Debug,
	)
	if err != nil {
		return fmt.Errorf("wunderbase: run query engine: %w", err)
	}

	slog.InfoCtx(ctx, "Server Listening", slog.String("addr", config.ListenAddr))
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
		return fmt.Errorf("wunderbase: close server: %w", err)
	}
	log.Println("Server stopped")
	wg.Done()
	wg.Wait()

	return nil
}

func initLogger(config *config) (err error) {
	// Enable debug logging, if set by the config.
	if config.Debug {
		LogLevel.Set(slog.LevelDebug)
	}

	opts := slog.HandlerOptions{Level: &LogLevel}

	if !config.Timestamp {
		opts.ReplaceAttr = removeTime
	}

	var handler slog.Handler
	switch format := config.LogFormat; format {
	case "text":
		handler = slog.NewTextHandler(os.Stderr, &opts)
	case "json":
		handler = slog.NewJSONHandler(os.Stderr, &opts)
	default:
		return fmt.Errorf("invalid log format: %q", format)
	}

	slog.SetDefault(slog.New(handler))
	return
}

func removeTime(groups []string, a slog.Attr) slog.Attr {
	if a.Key == slog.TimeKey {
		return slog.Attr{}
	}
	return a
}
