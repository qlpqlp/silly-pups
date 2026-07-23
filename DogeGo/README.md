# DogeGo PUP (silly-pups / DogeBox)

Installs **[DogeGo](https://github.com/qlpqlp/dogego)** (Go Dogecoin full node + web dashboard) on DogeBox via this silly-pups source.

Source is built from `https://github.com/qlpqlp/dogego` with `modRoot = "DogeGo"` (see [DogeGo app tree](https://github.com/qlpqlp/dogego/tree/main/DogeGo)).

There is **no DogeBox pup config UI**. After start, open the DogeGo web dashboard and complete the **setup wizard** (network, datadir, wallet, RPC, etc.). DogeGo writes `dogecoinconf.json` itself under the pup home (`/storage/dogego/.config/DogeGo/`).

## Install

1. On DogeBox, add this silly-pups repo as a pup source (if not already).
2. Install the **DogeGo** pup from the catalog.
3. Open the WebUI (port **2013**) and finish setup in-app.

## Layout

| File | Role |
|------|------|
| `manifest.json` | Pup metadata, ports, services, Dogebox metrics |
| `pup.nix` | Build DogeGo + metrics `monitor` + `run.sh` |
| `monitor/monitor.go` | Polls `GET /api/summary`, POSTs to `/dbx/metrics` |
| `logo.png` | Pup icon in DogeBox UI |

## Runtime

```text
dogego node -webui $DBX_PUP_IP:2013 -nobrowser
```

(`HOME` / `XDG_*` point at `/storage/dogego` so the wizard can save config. No `-datadir` and no pre-seeded `dogecoinconf.json`.)

## Ports

| Port | Use |
|------|-----|
| 2013 | Web dashboard (host) |
| 22556 | Mainnet P2P (host) |
| 44556 | Testnet P2P (host) |
| 22557 | Mainnet JSON-RPC (container) |
| 44555 | Testnet JSON-RPC (container) |

## Updating the upstream pin

When bumping `pup.nix` `fetchgit.rev`:

1. Update `rev` and recompute `src.hash` / `vendorHash` (Nix will print `got: sha256-...` on mismatch).
2. Recompute `manifest.json` → `container.build.nixFileSha256` (LF-normalized SHA-256 of `pup.nix`).

**PowerShell (LF-normalized):**

```powershell
$p = 'DogeGo\pup.nix'
$raw = [System.IO.File]::ReadAllText($p)
$lf = $raw -replace "`r`n", "`n"
$b = [System.Text.Encoding]::UTF8.GetBytes($lf)
$h = [System.Security.Cryptography.SHA256]::Create().ComputeHash($b)
($h | ForEach-Object ToString x2) -join ''
```
