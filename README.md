# Everlake launcher

The launcher for Project Everlake. It installs **one pinned build** of the WoW Forever client straight from
Blizzard's public CDN, checks every file against its hash, and repairs what's damaged. Later it also picks the
realm and starts the game.

It never follows Blizzard's live build: the build is pinned in [`internal/pin/wowf.json`](internal/pin/wowf.json)
and compiled in, so a launcher release always installs exactly the client the realm's server core supports.

## Starting it

**Double-click it.** The launcher opens as an app window with **Play**, **Install / update** and **Verify**, the
sign-in fields, and its output live. There's no console window and no browser tab. It uses the install folder
`C:\Everlake` on Windows and `~/Everlake` on macOS.

![The launcher's window on macOS](docs/images/launcher-window-macos.png)

| | File | |
|---|---|---|
| Windows | `everlake-launcher-windows-amd64.exe` | Double-click. Not signed yet: SmartScreen → *More info → Run anyway*. |
| Windows | `everlake-launcher-cli-windows-amd64.exe` | The same program for typing commands in PowerShell (below). |
| macOS | `Everlake-Launcher-macos.dmg` (or `.zip`) | Drag *Everlake Launcher* to Applications, double-click. Not notarised: the first time, macOS refuses; open it anyway from *System Settings → Privacy & Security → Open Anyway*. |

The window shows the launcher's own page (the one `ui --browser` shows) in the system's web view: WebView2 on
Windows (part of Windows 11, and of Windows 10 through Windows Update), WKWebView on macOS. A PC without WebView2
gets the same page in its browser instead, and the launcher ends a couple of minutes after that page is closed.

Closing the window ends the launcher, except while a game it joined to a realm is running: it then keeps going in the
background, keeping the realm's key in the game, and ends when the game closes.

## Status

Work in progress. What works today:

- `install` fetches the pinned build's configs, archive indexes, encoding file, install and download manifests, then
  the loose game files for your platform (the `.app` or `Wow.exe` and friends) and the data files, checking each
  one.
- The data files go into the game's own local storage, `Data/data` (`data.NNN` archives and `.idx` key tables, the
  layout a Battle.net install has), and `.build.info` names the build and the CDN. A folder an earlier launcher
  filled (with `.everlake/staged/`) is moved over without downloading again.
- `verify` re-checks what the install fetched (it reads `.everlake/install.json`), writes nothing, and lists what's
  damaged or missing; `install` again repairs exactly those. `verify --quick` is the fast check for every launch: it
  compares the client files (the loose files, configs, indexes, `.build.info`, `.flavor.info`) against the size and
  timestamp recorded at install, in a few milliseconds and without hashing. It doesn't deep-check the game's data
  storage (`Data/data`) — the full `verify` does that.
- A run that's stopped or killed resumes: the key tables are saved every 30 seconds, and a half-written archive
  with nothing pointing into it is removed.
- **Updating to a new pinned build** is the same `install`: it keeps every file whose key didn't change, fetches
  only the new ones, and once the new build is fully in place removes the old build's files and data (reclaiming the
  space). Nothing is removed until the update has succeeded. The game's own storage and anything it added are left
  alone. A trial install (`--no-data`, `--max-priority`, `--data-limit-mb`) never prunes data, so it can't delete
  what a fuller install fetched.
- **Joining a realm (Windows):** `play` signs in to the realm's login server and starts the game logged in, with the
  two in-memory changes the stock client needs to talk to it (see [Joining a realm](#joining-a-realm)). On macOS the
  game is hardened against this, so joining a realm stays with `wow-project-everlake`'s `everlake-client.sh`.

## Platforms

One binary per platform, no runtime to install:

- **Windows** (`GOOS=windows GOARCH=amd64`): installs into `<dir>\_forever_\Wow.exe`.
- **macOS** (`GOOS=darwin`, `arm64` and `amd64`): installs `<dir>/_forever_/World of Warcraft.app`, a proper
  universal `.app` with its executable bit set — the same folder a Battle.net install has, so
  `everlake-client.sh` can point at it instead of needing Battle.net.

`scripts/package.sh windows|macos [version]` builds the release files into `dist/`. The Windows exe is built with
`-H=windowsgui` (a GUI program: a double-click opens no console) and needs no cgo, so it builds anywhere. The Mac app
needs a Mac: its window uses Cocoa and WebKit through cgo, so it's built there for both architectures and joined into
one universal binary in `Everlake Launcher.app`. A Mac build made elsewhere (without cgo) works but shows the page in
the browser. CI builds both and keeps them as artifacts of each run.

Pick the platform's files with `--tags` (default: this machine). The launcher installs any platform's client from
any platform, so a Mac can prepare a Windows folder and the other way round.

## Commands

Every command still works from a terminal. On Windows use `everlake-launcher-cli-windows-amd64.exe` for that: the
window build prints into the terminal too, but as a GUI program the terminal doesn't wait for it (the prompt comes
back straight away), and `play` can't ask for the password properly. On macOS the app's binary is
`"/Applications/Everlake Launcher.app/Contents/MacOS/everlake-launcher"`.

```sh
go build -o everlake-launcher ./cmd/everlake-launcher

./everlake-launcher show                                    # the pinned build: tags, file counts, sizes
./everlake-launcher install --dir ~/Everlake --no-data      # the game's loose files only (~400 MB)
./everlake-launcher install --dir ~/Everlake                # everything for this machine (~85 GB)
./everlake-launcher verify  --dir ~/Everlake                # full hash check
./everlake-launcher verify  --dir ~/Everlake --quick        # size + timestamps only, for every launch
./everlake-launcher pin                                     # what's live now, as a pin to review
./everlake-launcher start   --dir ~/Everlake                # starts the game the way Battle.net would (-uid wowf)
./everlake-launcher                                         # the window, for ~/Everlake (what a double-click does)
./everlake-launcher ui      --dir ~/Everlake                # the window, for another folder
./everlake-launcher ui      --dir ~/Everlake --browser      # the same page in the browser instead
everlake-launcher.exe play --dir C:\Everlake --login-host login.realm.example.com --email you@example.com
```

`ui` is the same jobs with buttons instead of typed commands: a page served only on your own machine (`127.0.0.1`)
showing whether the client is intact, with **Install / update**, **Verify** and **Play**, and the launcher's output
live, in the launcher's window (`--browser`: in your browser; `--no-open`: just served). On Windows the page also has
the sign-in fields: with a login server filled in, **Play** joins that realm like `play` does; left empty, it starts
the game on its own like `start`.

`--tags` picks platform, architecture, locale and region (default: this machine, `enUS`, `US`). The region matters:
the Mac game binary for CN differs from everyone else's. `--max-priority` and `--data-limit-mb` cut the data stage
short for trial runs. `--cdn host[:port],...` tries those hosts first and Blizzard's after them (our mirror,
below); a host may name its scheme (`http://` is the default, `https://` also works).

## Joining a realm

`play` (Windows) does what Battle.net does for Blizzard's realms, against a TrinityCore login server:

1. Signs in with your email and password (asked for; or `EVERLAKE_PASSWORD`) over HTTPS on port 8081, and gets a
   login ticket and your game account. The login server needs a certificate your PC trusts (a real one, or pass
   `--ca` with the realm's own CA).
2. Leaves the ticket where the game reads it when started with `-launcherlogin`: `HKEY_CURRENT_USER\Software\Blizzard
   Entertainment\Battle.net\Launch Options\WoW` (`WEB_TOKEN`, encrypted for your Windows user; `GAME_ACCOUNT`;
   `CONNECTION_STRING`). Battle.net rewrites these whenever it starts WoW itself.
3. Writes `_forever_\WTF\Everlake.wtf` (your `Config.wtf` plus the login server) and starts
   `Wow.exe -uid wowf -launcherlogin -config Everlake.wtf`.
4. Changes two things in the running game's memory, both once at start-up, with the game paused just after its
   image unpacks (as Arctium's launcher does). The files on disk are never touched.
   - **The login name.** The client only logs in at names ending in one of Blizzard's domains (a table in its data,
     `.actual.battle.net` first). The ending is blanked, so a realm can use a name of its own. Not needed, and not
     done, for a `*.actual.battle.net` login name.
   - **The server key.** When you enter the world the realm signs a message the client checks against 12 public keys
     it keeps in memory. Entry 8 (the server's `Network.EnterEncryptedModeRegionGroup`) gets the realm's key:
     TrinityCore's stock key unless `--server-key` says otherwise. It's written over the game's built-in copy of that
     key, which the game builds its key store from when you log in. The key store itself is never changed: the live
     client's protection (`Wow_loader.dll`) crashes the game ("Security Crash") when it is. The launcher keeps reading
     the store and says in its log whether the realm's key is in it.

The login server, email, key and CA are saved in `.everlake/realm.json` in the install folder; the password never is.
The key store layout follows jibay442/forever_docker; where and when to patch follow Arctium's launcher (login name,
Ed25519 key). On macOS, `wow-project-everlake`'s `client/macos/keypatch.c` patches from inside the game.

## The mirror (our own CDN)

`mirror` runs on the server and answers like Blizzard's CDN, so a launcher on the same home network keeps installing
when Blizzard's CDN is down or slow (design and later steps: [docs/cdn-and-peers.md](docs/cdn-and-peers.md)). It is
for the local network only: it is never published to the internet. Players elsewhere download from Blizzard and,
later, from each other.

```sh
./everlake-launcher mirror --store /srv/cdn --prefetch               # serve on :8087, fetch the pinned build in the background
./everlake-launcher mirror --store /srv/cdn --plan-only              # how much the prefetch would fetch (88.3 GB for 70790)
./everlake-launcher install --dir ~/Everlake --cdn server:8087       # a launcher using it
```

- Every request is answered from `--overlay` (our own patched builds, served as they are), else the store, else
  Blizzard. What it gets from Blizzard is checked like an install checks it before it's kept or served, and what's in
  the store is checked again each time it's served, so a damaged copy is fetched again instead of handed out.
- Archives are kept by range (a sparse file plus a `.have` list of the ranges in it). The prefetch reads them in the
  launcher's own ranges, merged over Mac and Windows, so every range a launcher asks for is in the store.
- `--rate-mb` (default 40) caps the prefetch, `--memory-mb` (default 1024) the archive bytes held in memory at once.
  The mirror never forces writes to disk.
- The Everlake portal's compose file runs it as its `cdn` service (from this repo's `Dockerfile`).

## How it checks files

Every TACT file carries its own hash, and every parser here checks it:

| File | Check |
|---|---|
| build and CDN config | the name is the MD5 of the file |
| archive index | the name is the MD5 of its footer; the footer, table of contents and every block carry checksums |
| stored file (BLTE) | the encoded key is the MD5 of its header; the header lists an MD5 for every chunk |
| decoded file | the content key is the MD5 of the content (manifests, encoding file, loose game files) |
| encoding file | every page has an MD5 in the page index |
| local storage (`Data/data`) | each key table carries Jenkins checksums; each stored file is read back and checked as above |

So a file that parses is the file the pinned build names, byte for byte. Encrypted chunks are checked the same way
without being decrypted.

## Tests

```sh
go test ./...                                   # format parsers + a whole install against a fake CDN
                                                # (on Windows also: both game patches on a stand-in game process)
EVERLAKE_WINDOW_TEST=1 go test -run Window ./internal/appwindow   # Windows: opens a real WebView2 window
EVERLAKE_LIVE=1 go test -run Live ./internal/tact     # reads the real pinned build from Blizzard's CDN (~2 MB)
EVERLAKE_LIVE=full go test -run Live ./internal/tact  # also the encoding file (~135 MB)
```

## Licence

GPL-2.0, as the rest of Project Everlake.
