# Everlake launcher

The launcher for Project Everlake. It installs **one pinned build** of the WoW Forever client straight from
Blizzard's public CDN, checks every file against its hash, and repairs what's damaged. Later it also picks the
realm and starts the game.

It never follows Blizzard's live build: the build is pinned in [`internal/pin/wowf.json`](internal/pin/wowf.json)
and compiled in, so a launcher release always installs exactly the client the realm's server core supports.

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
- **Not yet:** connecting to our realm (login, the server key). `install` and `start` get the stock client running;
  joining an Everlake realm is done elsewhere (on macOS, `wow-project-everlake`'s `everlake-client.sh`).

## Platforms

One binary per platform, no runtime to install:

- **Windows** (`GOOS=windows GOARCH=amd64`): installs into `<dir>\_forever_\Wow.exe`.
- **macOS** (`GOOS=darwin`, `arm64` and `amd64`): installs `<dir>/_forever_/World of Warcraft.app`, a proper
  universal `.app` with its executable bit set — the same folder a Battle.net install has, so
  `everlake-client.sh` can point at it instead of needing Battle.net.

Pick the platform's files with `--tags` (default: this machine). The launcher installs any platform's client from
any platform, so a Mac can prepare a Windows folder and the other way round.

## Use

```sh
go build -o everlake-launcher ./cmd/everlake-launcher

./everlake-launcher show                                    # the pinned build: tags, file counts, sizes
./everlake-launcher install --dir ~/Everlake --no-data      # the game's loose files only (~400 MB)
./everlake-launcher install --dir ~/Everlake                # everything for this machine (~85 GB)
./everlake-launcher verify  --dir ~/Everlake                # full hash check
./everlake-launcher verify  --dir ~/Everlake --quick        # size + timestamps only, for every launch
./everlake-launcher pin                                     # what's live now, as a pin to review
./everlake-launcher start   --dir ~/Everlake                # starts the game the way Battle.net would (-uid wowf)
./everlake-launcher ui      --dir ~/Everlake                # a local page with Install / Verify / Play buttons
```

`ui` is the same jobs with buttons instead of typed commands: it opens a page in your browser (served only on your
own machine, `127.0.0.1`) showing whether the client is intact, with **Install / update**, **Verify** and **Play**,
and the launcher's output live. It still doesn't join a realm — `start`/`Play` get you to the game's own login
screen, no further.

`--tags` picks platform, architecture, locale and region (default: this machine, `enUS`, `US`). The region matters:
the Mac game binary for CN differs from everyone else's. `--max-priority` and `--data-limit-mb` cut the data stage
short for trial runs.

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
EVERLAKE_LIVE=1 go test -run Live ./internal/tact     # reads the real pinned build from Blizzard's CDN (~2 MB)
EVERLAKE_LIVE=full go test -run Live ./internal/tact  # also the encoding file (~135 MB)
```

## Licence

GPL-2.0, as the rest of Project Everlake.
