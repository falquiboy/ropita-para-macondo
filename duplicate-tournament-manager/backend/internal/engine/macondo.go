package engine

import (
    "bytes"
    "context"
    "encoding/json"
    "errors"
    "fmt"
    "path/filepath"
    "os"
    "os/exec"
    "strings"
    "time"

    "dupman/backend/internal/api"
)

// MacondoEngine shells out to an external Macondo-compatible binary to
// generate moves. You can provide a wrapper script that accepts a JSON
// request on stdin and prints a JSON response on stdout, matching
// api.MovesRequest/api.MovesResponse shapes.
//
// Required env:
//  - MACONDO_BIN: path to the macondo (or wrapper) executable
// Optional env:
//  - MACONDO_TIMEOUT_MS: per-request timeout (default 5000)
//
// Contract expected of the binary:
//  - Reads a JSON matching api.MovesRequest from stdin
//  - Writes a JSON matching api.MovesResponse to stdout
//  - Non-zero exit or invalid JSON is treated as error
type MacondoEngine struct {
    BinPath string
    Timeout time.Duration
}

func NewMacondoEngine(bin string, timeout time.Duration) (*MacondoEngine, error) {
    if bin == "" {
        return nil, errors.New("empty bin path")
    }
    if timeout <= 0 {
        timeout = 5 * time.Second
    }
    return &MacondoEngine{BinPath: bin, Timeout: timeout}, nil
}

func NewMacondoEngineFromEnv() (*MacondoEngine, error) {
    bin := os.Getenv("MACONDO_BIN")
    to := 5 * time.Second
    if s := os.Getenv("MACONDO_TIMEOUT_MS"); s != "" {
        if ms, err := time.ParseDuration(s + "ms"); err == nil {
            to = ms
        }
    }
    return NewMacondoEngine(bin, to)
}

func (m *MacondoEngine) GenAll(board api.Board, rack string, kwgPath, ruleset string) (api.MovesResponse, error) {
    if m.BinPath == "" {
        return api.MovesResponse{}, errors.New("MACONDO_BIN not configured")
    }
    req := api.MovesRequest{ Board: board, Rack: rack, KWGPath: kwgPath, Ruleset: ruleset }
    // Attempt to provide KLV2 if available via env KLV2_DIR or cwd
    if base := baseLexiconName(kwgPath); base != "" {
        if klv := findKLV2(base); klv != "" {
            req.KLV2Path = klv
        }
    }
    in, err := json.Marshal(req)
    if err != nil {
        return api.MovesResponse{}, err
    }

    ctx, cancel := context.WithTimeout(context.Background(), m.Timeout)
    defer cancel()
    cmd := exec.CommandContext(ctx, m.BinPath)
    cmd.Stdin = bytes.NewReader(in)
    var outBuf, errBuf bytes.Buffer
    cmd.Stdout = &outBuf
    cmd.Stderr = &errBuf
    // Ensure MACONDO_DATA_PATH is available to the wrapper
    env := os.Environ()
    hasDP := false
    for _, e := range env { if strings.HasPrefix(e, "MACONDO_DATA_PATH=") { hasDP = true; break } }
    if !hasDP {
        // Try locations relative to current working dir
        cands := []string{"../../macondo/data", "../macondo/data", "macondo/data"}
        // Also try relative to wrapper binary location
        base := filepath.Dir(m.BinPath)
        cands = append(cands,
            filepath.Join(base, "..", "..", "..", "macondo", "data"), // backend/bin -> ../../.. -> DUPMAN
            filepath.Join(base, "..", "..", "..", "..", "macondo", "data"), // extra fallback
        )
        for _, p := range cands {
            if st, err := os.Stat(p); err == nil && st.IsDir() {
                if abs, err2 := filepath.Abs(p); err2 == nil { p = abs }
                env = append(env, "MACONDO_DATA_PATH="+p)
                hasDP = true
                break
            }
        }
    }
    cmd.Env = env
    if err := cmd.Run(); err != nil {
        if ctx.Err() == context.DeadlineExceeded {
            return api.MovesResponse{}, fmt.Errorf("macondo timeout after %v", m.Timeout)
        }
        return api.MovesResponse{}, fmt.Errorf("macondo exec error: %v (stderr: %s)", err, errBuf.String())
    }

    var res api.MovesResponse
    if err := json.Unmarshal(outBuf.Bytes(), &res); err != nil {
        return api.MovesResponse{}, fmt.Errorf("invalid macondo json: %w (stdout: %s)", err, outBuf.String())
    }
    return res, nil
}

func baseLexiconName(kwgPath string) string {
    if kwgPath == "" { return "" }
    // strip directory and extension
    var base string
    for i := len(kwgPath)-1; i >= 0; i-- {
        if kwgPath[i] == '/' || kwgPath[i] == '\\' { base = kwgPath[i+1:]; break }
    }
    if base == "" { base = kwgPath }
    // remove extension
    for i := len(base)-1; i >= 0; i-- {
        if base[i] == '.' { return base[:i] }
    }
    return base
}

func findKLV2(base string) string {
    // Try KLV2_DIR env
    if dir := os.Getenv("KLV2_DIR"); dir != "" {
        p := dir + "/" + base + ".klv2"
        if _, err := os.Stat(p); err == nil { return p }
    }
    // Try cwd
    if _, err := os.Stat(base + ".klv2"); err == nil { return base + ".klv2" }
    // Try parent of backend (../../<base>.klv2) when running from backend/
    if _, err := os.Stat("../../" + base + ".klv2"); err == nil { return "../../" + base + ".klv2" }
    // Try lexica subfolder variants
    if _, err := os.Stat("lexica/" + base + ".klv2"); err == nil { return "lexica/" + base + ".klv2" }
    if _, err := os.Stat("../lexica/" + base + ".klv2"); err == nil { return "../lexica/" + base + ".klv2" }
    if _, err := os.Stat("../../lexica/" + base + ".klv2"); err == nil { return "../../lexica/" + base + ".klv2" }
    // Try repo-root relative to executable
    if ex, err := os.Executable(); err == nil {
        // backend/internal/engine → repo-root at ../../..
        p := ex
        // no robust path join to avoid extra imports; rely on env approach primarily
        _ = p
    }
    return ""
}
