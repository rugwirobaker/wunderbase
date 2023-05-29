package queryengine

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"syscall"

	"golang.org/x/exp/slog"
)

func Run(ctx context.Context, wg *sync.WaitGroup, queryEnginePath, queryEnginePort, prismaSchemaFilePath string, production, debug bool) error {
	// when start prisma query engine ,
	// we're not able to listen on the same port,
	// if last engine instance still alive.
	// so we must kill the existing engine process before we start new onw.

	args := []string{"--datamodel-path", prismaSchemaFilePath}
	if !production {
		// killExistingPrismaQueryEngineProcess(queryEnginePort)
		args = append(args, "--enable-playground", "--port", queryEnginePort)
	}
	if debug {
		args = append(args, "--debug", "--log-queries")
	}

	cmd := exec.CommandContext(ctx, queryEnginePath, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("error creating StdoutPipe for Cmd: %w", err)
	}
	defer stdout.Close()

	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			slog.InfoCtx(ctx, scanner.Text(), slog.String("process", "query-engine"))
		}
	}()

	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("error creating StderrPipe for Cmd: %w", err)
	}
	defer stderr.Close()

	go func() {
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			slog.ErrorCtx(ctx, scanner.Text(), slog.String("process", "query-engine"))
		}
	}()

	err = cmd.Start()
	if err != nil {
		return fmt.Errorf("error starting Cmd: %w", err)
	}

	go func() {
		<-ctx.Done()

		err = cmd.Process.Kill()
		if err != nil {
			slog.ErrorCtx(ctx, "killing query engine", err, slog.String("process", "query-engine"))
		}
		slog.InfoCtx(ctx, "query engine stopped")

		wg.Done()
	}()
	return nil
}

// reference:https://github.com/wundergraph/wundergraph
func killExistingPrismaQueryEngineProcess(queryEnginePort string) {
	var err error
	if runtime.GOOS == "windows" {
		command := fmt.Sprintf("(Get-NetTCPConnection -LocalPort %s).OwningProcess -Force", queryEnginePort)
		_, err = execCmd(exec.Command("Stop-Process", "-Id", command))
	} else {
		command := fmt.Sprintf("lsof -i tcp:%s | grep LISTEN | awk '{print $2}' | xargs kill -9", queryEnginePort)
		if command == "" {
			return
		}

		var data []byte
		data, err = execCmd(exec.Command("sh", "-c", command))
		if err == nil && len(data) > 0 {
			_, err = execCmd(exec.Command("kill", "-9", strings.TrimSpace(string(data))))
		}
	}
	if err != nil {
		var waitStatus syscall.WaitStatus
		if exitError, ok := err.(*exec.ExitError); ok {
			waitStatus = exitError.Sys().(syscall.WaitStatus)
			slog.Error("Error killing prisma query", slog.Any("err", err), slog.Int("exit code", waitStatus.ExitStatus()))
		}
	}
}

func execCmd(cmd *exec.Cmd) ([]byte, error) {
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	// Connecting Stderr can help debugging when something goes wrong
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return nil, err
	}
	return stdout.Bytes(), nil
}
