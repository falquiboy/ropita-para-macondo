package api

import (
    "io"
    "net/http"
    "os"
    "path/filepath"
)

// serveKLV2 serves a raw KLV2 file by name from a few likely locations.
// Priority:
//  1) KLV2_DIR env var directory
//  2) current working directory
//  3) repo-root relative to executable (../../../../)
func serveKLV2(w http.ResponseWriter, r *http.Request) {
    name := chiURLParam(r, "name")
    if name == "" {
        http.Error(w, "missing name", http.StatusBadRequest)
        return
    }
    // Candidates
    cand := []string{}
    if dir := os.Getenv("KLV2_DIR"); dir != "" {
        cand = append(cand, filepath.Join(dir, name))
    }
    // Current working directory
    cand = append(cand, name)
    // Try common repo roots relative to backend (../../name) or project (../name)
    if p := findRootFile(name); p != "" { cand = append(cand, p) }
    if ex, err := os.Executable(); err == nil {
        base := filepath.Dir(ex)
        cand = append(cand, filepath.Join(base, "..", "..", "..", "..", name))
    }
    // Try lexica subfolders relative to cwd
    cand = append(cand, filepath.Join("lexica", name))
    cand = append(cand, filepath.Join("..", "lexica", name))
    cand = append(cand, filepath.Join("..", "..", "lexica", name))
    var f *os.File
    var err error
    for _, p := range cand {
        if p == "" { continue }
        if f, err = os.Open(p); err == nil {
            defer f.Close()
            w.Header().Set("Content-Type", "application/octet-stream")
            w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate, max-age=0")
            if _, err := io.Copy(w, f); err != nil {
                http.Error(w, err.Error(), http.StatusInternalServerError)
            }
            return
        }
    }
    http.Error(w, "klv2 not found", http.StatusNotFound)
}

// chiURLParam avoids importing chi here; router already depends on chi.
func chiURLParam(r *http.Request, key string) string {
    // Cheap parse of the last segment: /klv2/{name}
    p := r.URL.Path
    i := len(p) - 1
    for i >= 0 && p[i] != '/' { i-- }
    if i >= 0 && i+1 < len(p) { return p[i+1:] }
    return ""
}
