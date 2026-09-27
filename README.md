# Steam → Apollo Exporter

A single Windows `.exe` with a GUI that exports your installed **Steam** games to
**[Apollo](https://github.com/ClassicOldSong/Apollo)**, [Sunshine](https://github.com/LizardByte/Sunshine), or a fork such as
[Vibepollo](https://github.com/Nonary/Vibepollo). The games then show up in Moonlight/Artemis with their launch commands and cover art.

## Features

- **All Steam libraries:** the exe finds Steam through the registry and scans every library folder listed in `libraryfolders.vdf`, the same list as Steam → Settings → Storage.
  Each game card shows which drive it's installed on.
- **Cover art:** it uses the portrait art from Steam's local library cache, falls back to Steam's CDN when that's missing,
  and converts it to PNG because Apollo only accepts PNG.
- **Host detection:** it finds Apollo/Sunshine through its Windows service, so it works even when the installer's registry entry has no install path.
  It respects a `file_apps` override in `sunshine.conf`. If several hosts are installed, choose one in the **Target** dropdown.
- **Safe edits:** only Steam entries are added or removed, and your other apps and settings are left as they are.
  Before each save, the previous file is copied to `apps.json.bak`.
- **Search and filters:** search by name or app ID, and filter by *All / Not in Apollo / In Apollo*.
  Redistributables, SteamVR and runtimes stay hidden unless you tick *Show tools & runtimes*.
- **Auto-restart:** the exe can restart the host service afterwards, because Apollo only re-reads `apps.json` on restart.
  A restart disconnects any active stream.

## Usage

1. Download `SteamApolloExporter.exe` from the [latest release](https://github.com/Sabissimo/SteamApolloExporter/releases/latest), run it, and accept the administrator prompt.
   It needs admin because Apollo's config lives in `Program Files` and restarting the service requires it.
2. Click games to select them, or use **Select all shown**.
3. Click **Add to Apollo**, or click **Remove from Apollo** to take them out again.

For each game you add, Apollo gets an entry like this:

```json
{
    "name": "DARK SOULS™ III",
    "cmd": "steam://rungameid/374320",
    "image-path": "C:\Program Files\Apollo\config\covers\steam_374320.png",
    "auto-detach": true,
    "wait-all": true,
    "exit-timeout": 5,
    "uuid": "…"
}
```

Covers are saved in `<Apollo>\config\covers\steam_<appid>.png`. Removing a game also deletes its cover.

## Requirements

- Windows 10/11 with the Microsoft Edge WebView2 Runtime (included with Windows 11)
- Steam
- Apollo, Sunshine, or a fork, installed as a service

## Building

Requires Go 1.26+. No C compiler or cgo is needed.

```powershell
./build.ps1        # embeds icon + admin manifest, then builds SteamApolloExporter.exe
```

Project layout:

| Path | Purpose |
|---|---|
| `main.go` | Starts the WebView2 window and a token-protected localhost server |
| `server.go` | JSON API: scan state, cover images, and add/remove |
| `internal/steam` | Steam root, library folders, installed games, cover lookup |
| `internal/apollo` | Host detection via services, `apps.json` editing, service restart |
| `internal/vdf` | Parser for Valve's text KeyValues format (`.vdf` / `.acf`) |
| `ui/index.html` | The interface, embedded into the exe |
| `winres/`, `rsrc_windows_amd64.syso` | Icon and "require administrator" manifest |

## License

[GPL-3.0](LICENSE) © George Sabanadze
