# DupMan — Setup en Windows

## 1. Instalar prerequisitos

### Go 1.24+ (obligatorio)
- Descarga el `.msi` de https://go.dev/dl/ (Windows amd64)
- Instala con defaults
- Reinicia tu terminal después de instalar
- Verifica: `go version`

### Git (si no lo tienes)
- https://git-scm.com/download/win
- Instala con defaults

### Rust (opcional, solo para wolges/hybrid engine)
- https://rustup.rs/
- Descarga e instala rustup-init.exe
- Reinicia terminal, verifica: `cargo --version`

### Claude Code (para seguir desarrollando)
- `npm install -g @anthropic-ai/claude-code` (requiere Node.js 18+)
- Si no tienes Node: https://nodejs.org/

## 2. Clonar el repo

```powershell
cd C:\Users\TuUsuario\Projects   # o donde prefieras
git clone https://github.com/falquiboy/ropita-para-macondo.git
cd ropita-para-macondo
```

## 3. Permitir scripts PowerShell (una sola vez)

```powershell
Set-ExecutionPolicy -Scope CurrentUser RemoteSigned
```

## 4. Arrancar el servidor

```powershell
.\start_go_server.ps1 8090
```

La primera vez tarda más porque Go descarga dependencias y compila todo.

Abre `http://localhost:8090` en tu browser.

## 5. Flags opcionales

```powershell
.\start_go_server.ps1 8090 -Fg              # foreground (logs en consola)
.\start_go_server.ps1 8090 -Watch           # auto-rebuild al detectar cambios
.\start_go_server.ps1 8090 -ValidateSpan    # validar palabras contra KWG
```

## 6. Estructura relevante

```
ropita-para-macondo/
├── start_go_server.ps1          ← script Windows
├── start_go_server.sh           ← script Mac/Linux
├── duplicate-tournament-manager/
│   └── backend/
│       ├── cmd/server/          ← servidor Go
│       ├── cmd/macondo-wrapper/ ← wrapper del engine
│       ├── internal/api/        ← handlers + frontend embebido
│       └── bin/                 ← binarios compilados (.exe)
├── macondo/                     ← engine + datos
├── wolges/                      ← engine alternativo (Rust)
└── lexica/                      ← archivos .kwg y .klv2
```

## 7. Troubleshooting

**"go: not found"** → Reinstala Go, asegúrate de que `C:\Go\bin` esté en PATH

**"cannot be loaded because running scripts is disabled"** → Ejecuta el paso 3

**Puerto ocupado** → El script lo libera automáticamente, pero si falla: `Get-NetTCPConnection -LocalPort 8090` para ver qué proceso lo usa

**"macondo-wrapper build failed"** → Verifica que Go compile: `go version`. Si hay errores de módulos, borra `.gocache` y `.gomodcache` y reintenta

**Simulaciones no funcionan** → Verifica que el log diga `Using engine: macondo`. Si dice `stub`, el wrapper no se compiló bien
