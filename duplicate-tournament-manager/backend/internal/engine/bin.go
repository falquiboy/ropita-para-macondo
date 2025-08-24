package engine

import (
    "bytes"
    "context"
    "encoding/json"
    "errors"
    "fmt"
    "os/exec"
    "time"

    "dupman/backend/internal/api"
)

// BinEngine shells out to an external binary that implements the MovesRequest/MovesResponse contract.
type BinEngine struct {
    BinPath string
    Timeout time.Duration
}

func NewBinEngine(bin string, timeout time.Duration) (*BinEngine, error) {
    if bin == "" { return nil, errors.New("empty bin path") }
    if timeout <= 0 { timeout = 5 * time.Second }
    return &BinEngine{BinPath: bin, Timeout: timeout}, nil
}

func (b *BinEngine) GenAll(board api.Board, rack string, kwgPath, ruleset string) (api.MovesResponse, error) {
    req := api.MovesRequest{ Board: board, Rack: rack, KWGPath: kwgPath, Ruleset: ruleset }
    in, err := json.Marshal(req)
    if err != nil { return api.MovesResponse{}, err }
    ctx, cancel := context.WithTimeout(context.Background(), b.Timeout)
    defer cancel()
    cmd := exec.CommandContext(ctx, b.BinPath)
    cmd.Stdin = bytes.NewReader(in)
    var outBuf, errBuf bytes.Buffer
    cmd.Stdout = &outBuf
    cmd.Stderr = &errBuf
    if err := cmd.Run(); err != nil {
        if ctx.Err() == context.DeadlineExceeded {
            return api.MovesResponse{}, fmt.Errorf("engine timeout after %v", b.Timeout)
        }
        return api.MovesResponse{}, fmt.Errorf("engine exec error: %v (stderr: %s)", err, errBuf.String())
    }
    var res api.MovesResponse
    if err := json.Unmarshal(outBuf.Bytes(), &res); err != nil {
        return api.MovesResponse{}, fmt.Errorf("invalid engine json: %w (stdout: %s)", err, outBuf.String())
    }
    return res, nil
}

