# Our own CDN and peer-to-peer downloads

Status: design agreed 2026-11-07. Step 1 (the mirror) built 2026-11-08; the rest not started.

The launcher installs the pinned Forever build from Blizzard's CDN. That breaks when Blizzard's CDN is down or
limits us, and it is slow when another player on the same network already has every file. This adds two more
places to get files from:

- **The mirror**: a CDN of our own on the Everlake server. It keeps a copy of everything it fetches from Blizzard,
  and downloads the pinned build ahead of time, so a fresh install never needs Blizzard.
- **Peers**: other launchers. First on the same network (LAN), later over the internet.

Every launcher tries them in this order: LAN peers, then the mirror, then Blizzard (internet peers later go between
the mirror and Blizzard).

## Why this is safe

Every file on Blizzard's CDN is named by a hash of its own bytes (its *encoded key*). The launcher already checks
each file against that hash (`tact.VerifyBLTE`) and tries the next host when a copy is wrong (`cdn.Request.Check`).
So a copy from the mirror or from a stranger is exactly as trustworthy as one from Blizzard: a bad copy fails the
check, is thrown away, and the next source is asked. The mirror checks too, before it keeps anything.

The things a peer could still do wrong are to send nothing, send slowly, or lie about having a file. Each of those
only costs time: the launcher moves on.

## Sizes (pinned build 1.60.1.70790, measured with `everlake-launcher show`)

| Copy | Files | Stored |
|---|---|---|
| Mac (OSX x86_64 enUS US) | 1,144,116 | 84.7 GB |
| Windows (Windows x86_64 enUS US) | 1,144,516 | 84.6 GB |

The two share nearly every file. The prefetch for both (`mirror --plan-only`, tags OSX, Windows, x86_64, arm64,
enUS, US) is 1,145,694 files in 33,436 requests, 88.3 GB with the gaps between files, plus about 300 MB of indexes.
Other languages are left out until someone needs one.

## One store, one protocol

The mirror and the peers are the same idea: something that holds files by encoded key and hands them out. So they
share one store and one protocol.

- **Store.** A player's launcher already has every file in the game's own storage (`Data/data`), readable by key
  (`casc.Storage.Read`). It serves from there and needs no extra disk. The mirror uses the same storage format
  for its own copy.
- **Protocol** (HTTP, under `/everlake/v1/`):
  - `GET /everlake/v1/key/<ekey>` returns one file's bytes, as Blizzard's CDN would.
  - `POST /everlake/v1/keys` with a list of keys returns those it has, one after another, each with its key and
    length. One request fetches hundreds of small files, which matters with 1.1 million of them.
  - `POST /everlake/v1/have` with a list of keys answers which ones it holds, so a launcher can split work
    between peers.
  - `GET /everlake/v1/info` gives the product, the builds it holds and how busy it is.
- Peers serve only files they hold by key: no paths, no listing of the disk. Serving is off unless the launcher is
  open, and has an upload cap and an off switch.

## The mirror

A new command, `everlake-launcher mirror`, runs on Patronus as the `cdn` service of the Everlake portal's compose
file, with its store in the portal's folder on the NVMe (`/mnt/NVME/docker/everlake-portal/cdn`). It replaces
wow-project-everlake's `tools/tact/cdn.py`, which passes everything through to Blizzard and keeps nothing.

That disk also holds the game's database and has stalled on sync writes before, so the mirror never forces a
write (no fsync): a file a crash damages fails its check when next served and is fetched again. The prefetch is
capped (`--rate-mb`, default 40).

It answers on two faces:

1. **Blizzard's CDN layout** (`/tpr/wow/config/..`, `/tpr/wow/data/..`, with byte ranges). Old launchers, the
   Mac mini scripts and the game itself can then use it as a normal CDN host.
   - A whole file or config it has: served from the store.
   - A byte range inside an archive: the mirror keeps archives as sparse files with a list of the ranges stored.
     A stored range is served after checking every file wholly inside it against the archive index; otherwise the
     range is fetched from Blizzard, checked the same way, served, and kept (less any file cut off at its ends).
   - Missing: fetched from Blizzard, checked, kept, served. If Blizzard fails, the answer is an error, never
     unchecked bytes.
2. **The peer protocol** above, from the same store, with Blizzard behind it.

Our own patched builds (the China Undead and gate-2 test builds that `cdn-build` writes) are served first, before
the store, as `cdn.py` does now.

`everlake-launcher mirror --prefetch` fetches the pin's files for the chosen tags (default: Mac + Windows, enUS)
into the store in the background while serving (`--prefetch-only` fetches and stops). It reads archives in the
launcher's own ranges (`cdn.SpanGap`), merged over all the tags, so every range a launcher with some of those tags
asks for is stored. That holds for a launcher whose `--tags` names a platform, architecture, locale and region (the
default does); one that leaves a type out takes all of that type, more than the prefetch fetched. Run it once per
new pin; a stopped run picks up where it left off, and a request that fails is retried on the next run rather than
stopping this one.

Passed through and not kept: ranges of non-archives, ranges over 256 MB, several ranges in one request, whole
archives, and paths other than configs, indexes and data files. Passed-through streams have their own slots, so slow
clients can't hold up checked fetches. Archive ranges held in memory are capped over all requests together
(`--memory-mb`, default 1024).

A stored range that starts or ends inside a file is served only after the whole of those files is read and checked;
the store mends itself when its `.have` can't be read or its archive file is shorter than the `.have` says (those
ranges are fetched again).

## The game itself

The running game also streams files it doesn't have from the hosts in `.build.info` ("CDN Hosts"). The launcher
writes that file, so it can put the mirror first and Blizzard after it; the game then streams from us too. The
game uses plain HTTP on port 80, so the mirror needs a name on port 80 (a Traefik route on Patronus). Its name
comes from the player's realm settings, never from this public repo, as with the login host.

Later, the launcher can be the game's CDN itself (a small server on 127.0.0.1 while the game runs), so in-game
streaming also gets LAN peers. Not in the first steps.

## LAN peers

- **Finding each other**: every open launcher sends a small UDP broadcast/multicast message on the local
  network every few seconds: "I am an Everlake peer, this port, these builds". A launcher that is installing
  listens and builds a peer list. Nothing leaves the network.
- **Fetching**: the install's file list is split into batches. Each batch goes to a LAN peer that has the files
  (asked with `/have`), and whatever a peer can't give goes on to the mirror and then Blizzard. A peer that sends
  a bad file is dropped for the rest of the run.
- The game must not be writing `Data/data` while it is served. The launcher already opens storage read-only for
  this (`casc.OpenReadOnly`); serving waits while an install of its own is running.

## Internet peers (later)

A tracker on the Everlake portal, behind its invite-only sign-in: launchers tell it their address and builds, and
get back a few peers. That needs UPnP or a forwarded port on the player's side, an upload cap, and the
same-public-IP trick Evermore's tracker uses (players behind one router get each other's LAN address). Planned
after the mirror and LAN peers work.

## Steps

1. **Mirror, Blizzard layout** — `mirror` command, store, pass-through with checks and keeping, archive ranges,
   our patched builds first, `--prefetch`. Compose service on Patronus in place of `cdn`. The launcher gets a
   mirror host setting that goes first in its host list (`pin.Hosts` already takes extra hosts for this).
   Done when: an install on a fresh folder with Blizzard's hosts blocked completes from the mirror alone.
2. **Peer protocol** — `/everlake/v1/` on the mirror, and the launcher fetching through it in batches.
3. **LAN peers** — launchers serve their own storage, find each other, fetch from each other first.
   Done when: a second PC on the network installs mostly from the first (the run's report says how much came
   from where).
4. **The game streams from the mirror** — `.build.info` lists the mirror first; Traefik route on port 80.
5. **Internet peers** — portal tracker, UPnP, upload cap.

## Open questions

- Free space on the NVMe for about 89 GB (a 2 TB drive shared with the game server and its database).
- The mirror's public name on port 80, and whether it can go through Cloudflare (Cloudflare's rules frown on
  sending tens of GB of game files through its free proxy, so probably a DNS-only name).
- Whether the mirror's peer protocol should need the portal sign-in, or stay open like Blizzard's CDN. Open is
  simpler and the files are Blizzard's public files; our own patched builds are also served openly today.
