package migrate

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"os/exec"
	"time"
)

type MigrationRequest struct {
	Id      int                    `json:"id"`
	Jsonrpc string                 `json:"jsonrpc"`
	Method  string                 `json:"method"`
	Params  MigrationRequestParams `json:"params"`
}

type MigrationRequestParams struct {
	Force  bool   `json:"force"`
	Schema string `json:"schema"`
}

type MigrationResponse struct {
	Jsonrpc string                   `json:"jsonrpc"`
	Result  *MigrationResponseResult `json:"result,omitempty"`
	Error   *MigrationResponseError  `json:"error,omitempty"`
}

type MigrationResponseResult struct {
	ExecutedSteps int `json:"executedSteps"`
}

type MigrationResponseError struct {
	Code    int                        `json:"code"`
	Message string                     `json:"message"`
	Data    MigrationResponseErrorData `json:"data"`
}

type MigrationResponseErrorData struct {
	IsPanic bool                           `json:"is_panic"`
	Message string                         `json:"message"`
	Meta    MigrationResponseErrorDataMeta `json:"meta"`
}

type MigrationResponseErrorDataMeta struct {
	FullError string `json:"full_error"`
}

func Database(migrationEnginePath, migrationLockFilePath, schema, schemaPath string) error {
	h := sha256.New()
	expected := h.Sum([]byte(schema))
	lock, err := ioutil.ReadFile(migrationLockFilePath)
	if err != nil {
		return fmt.Errorf("read lock file: %v", err)
	}
	if bytes.Equal(lock, expected) {
		log.Println("Migration already executed, skipping")
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
	defer cancel()
	cmd := exec.CommandContext(ctx, migrationEnginePath, "--datamodel", schemaPath)
	in, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("migration engine std in pipe: %v", err)
	}
	defer in.Close()

	req := MigrationRequest{
		Id:      1,
		Jsonrpc: "2.0",
		Method:  "schemaPush",
		Params: MigrationRequestParams{
			Force:  true,
			Schema: schema,
		},
	}

	data, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshal migration request: %v", err)
	}
	data = append(data, []byte("\n")...)
	_, err = in.Write(data)
	if err != nil {
		return fmt.Errorf("write data to stdin: %v", err)
	}

	out, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("migration std out pipe: %v", err)
	}

	errs := make(chan error, 1) // Create a buffered error channel
	cmdDone := make(chan struct{}, 1)

	defer close(errs)

	go func() {
		defer close(cmdDone)
		err := cmd.Run()
		if err != nil && ctx.Err() == nil {
			errs <- fmt.Errorf("migration engine run: %v", err)
		}
	}()

	var resp MigrationResponse
	go func() {
		defer close(errs)
		r := bufio.NewReader(out)
		outBuf := &bytes.Buffer{}
		for {
			b, err := r.ReadByte()
			if err != nil {
				errs <- fmt.Errorf("migration ReadByte: %v", err)
				return
			}
			err = outBuf.WriteByte(b)
			if err != nil {
				errs <- fmt.Errorf("migration writeByte: %v", err)
				return
			}
			if b == '\n' {
				cancel()
				err = json.Unmarshal(outBuf.Bytes(), &resp)
				if err != nil {
					errs <- fmt.Errorf("migration unmarshal response: %v", err)
					return
				}
				return
			}
		}
	}()

	// Check if goroutine encountered any errors
	if err := <-errs; err != nil {
		return err
	}

	if resp.Error == nil {
		log.Println("Migration successful, updating lock file")
		err = ioutil.WriteFile(migrationLockFilePath, expected, 0644)
		if err != nil {
			return fmt.Errorf("migration write lock file: %v", err)
		}
		return nil
	} else {
		pretty, err := json.MarshalIndent(resp, "", "  ")
		if err != nil {
			return fmt.Errorf("migration marshal error: %v", err)
		}
		log.Printf("Migration failed:\n%s", string(pretty))
		err = ioutil.WriteFile(migrationLockFilePath, expected, 0644)
		if err != nil {
			return fmt.Errorf("migration write lock file: %v", err)
		}
		return fmt.Errorf("migration failed: %v", string(pretty))
	}
}
