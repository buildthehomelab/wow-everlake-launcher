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
  damaged or missing; `install` again repairs exactly those.
- A run that's stopped or killed resumes: the key tables are saved every 30 seconds, and a half-written archive
  with nothing pointing into it is removed.
- **Not yet:** starting the game on our realm (login, the server key patch). Whether the game starts from this
  folder at all is the next test.

## Use

```sh
go build -o everlake-launcher ./cmd/everlake-launcher

./everlake-launcher show                                    # the pinned build: tags, file counts, sizes
./everlake-launcher install --dir ~/Everlake --no-data      # the game's loose files only (~400 MB)
./everlake-launcher install --dir ~/Everlake                # everything for this machine (~85 GB)
./everlake-launcher verify  --dir ~/Everlake
./everlake-launcher pin                                     # what's live now, as a pin to review
```

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
