"""Prepare pinned CDN donors and run the Era map downloader."""

import argparse
from concurrent.futures import ThreadPoolExecutor
import json
import os
import shutil
from pathlib import Path
import sqlite3
import subprocess
import sys


PINS = {
    "classic-era-cache": ("5604ce35a8644bdcf0e26c1daf979c15", "1.15.9.70576"),
    "tbc-cache": ("68581f70e323e88b2c44c928ffce7cd8", "2.5.6.70577"),
    "titan-cache": ("eed3978f9e962450c5a358ef0dcbcf64", "3.80.2.70522"),
}
LISTFILE_URL = "https://github.com/wowdev/wow-listfile/releases/latest/download/community-listfile.csv"
TOOLS = Path(__file__).resolve().parent
TACT_TOOLS = TOOLS


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("workspace", type=Path)
    parser.add_argument("--status", action="store_true")
    args = parser.parse_args()
    sources = args.workspace.resolve()
    if args.status:
        log = sources / "logs/catalog.log"
        print(f"{sum(1 for _ in sources.glob('maps/*/*/inventory.json'))} map inventories in {sources}")
        if log.exists():
            print("".join(log.read_text().splitlines(keepends=True)[-12:]), end="")
        return 0

    if shutil.which("pgrep"):
        running = subprocess.run(
            ["pgrep", "-f", "fetch_map_catalog[.]sh"], capture_output=True, check=False
        )
        if running.returncode == 0:
            parser.error("a map catalog download is already running; use the status command")

    sources.mkdir(parents=True, exist_ok=True)
    with (sources / ".launcher.lock").open("a+b") as lock:
        try:
            if os.name == "nt":
                import msvcrt

                lock.seek(0)
                msvcrt.locking(lock.fileno(), msvcrt.LK_NBLCK, 1)
            else:
                import fcntl

                fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except OSError:
            parser.error("map collection is already running from the launcher")
        return download(TACT_TOOLS, sources)


def download(tact_tools, sources):
    listfile = sources / "community-listfile.csv"
    if not listfile.is_file():
        temporary = listfile.with_suffix(".part")
        subprocess.run(["curl.exe" if os.name == "nt" else "curl", "-fsSL", "--retry", "2", "-o", temporary, LISTFILE_URL], check=True)
        temporary.replace(listfile)

    maps = {}
    with listfile.open(encoding="utf-8", errors="replace") as lines:
        for line in lines:
            file_id, _, path = line.strip().partition(";")
            parts = path.lower().split("/")
            if len(parts) == 4 and parts[:2] == ["world", "maps"] and parts[3] == parts[2] + ".wdt":
                maps[int(file_id)] = path

    for donor, (build, version) in PINS.items():
        cache = sources / donor
        tact = cache / "tact"
        tact.mkdir(parents=True, exist_ok=True)
        pin = tact / "build.txt"
        if not pin.exists():
            pin.write_text(
                f"{build}\n8ecf609471cb2ea16667be9cc2b7a555\n{version}\n"
                "tpr/wow\nlevel3.blizzard.com us.cdn.blizzard.com\n"
            )
        elif pin.read_text().splitlines()[0] != build:
            raise SystemExit(f"Unexpected donor pin in {pin}")
        subprocess.run([sys.executable, tact_tools / "tact.py", "--cache", cache], check=True)
        with sqlite3.connect(tact / f"{build}.sqlite") as db:
            candidates = sorted(
                (file_id for (file_id,) in db.execute("SELECT DISTINCT fdid FROM files") if file_id in maps),
                key=maps.get,
            )
        (sources / f"{donor}-map-candidates.tsv").write_text(
            "".join(f"{file_id}\t{maps[file_id]}\n" for file_id in candidates)
        )

    logs = sources / "logs"
    logs.mkdir(exist_ok=True)
    catalog_log = logs / "catalog.log"
    print(f"Collecting maps in {sources}. Progress is written to {catalog_log}.", flush=True)
    with ThreadPoolExecutor(max_workers=len(PINS)) as pool:
        results = list(pool.map(lambda donor: fetch_donor(tact_tools, listfile, logs, donor, sources), PINS))
    return int(any(results))


def fetch_donor(tact_tools, listfile, logs, donor, sources):
    donor_logs = logs / donor
    donor_logs.mkdir(exist_ok=True)
    build, _ = PINS[donor]
    failures = 0
    candidates = sources / f"{donor}-map-candidates.tsv"
    with (logs / "catalog.log").open("a", encoding="utf-8", buffering=1) as catalog:
        for line in candidates.read_text().splitlines():
            wdt, path = line.split("\t", 1)
            inventory = sources / "maps" / build / wdt / "inventory.json"
            if inventory.exists():
                report = json.loads(inventory.read_text())
                if "unresolved_paths" in report:
                    failures += bool(report["failures"] or report["unresolved_paths"])
                    continue
            print(f"{donor} {wdt} {path}", file=catalog)
            print(f"Collecting {path} from {donor.removesuffix('-cache')}", flush=True)
            with (donor_logs / f"{wdt}.log").open("wb") as log:
                result = subprocess.call(
                    [sys.executable, "-u", TOOLS / "fetch_map_tiles.py",
                     "--tact-tools", tact_tools, "--cache", sources / donor,
                     "--wdt", wdt, "--out", sources / "maps", "--listfile", listfile],
                    stdout=log, stderr=subprocess.STDOUT,
                )
            failures += bool(result)
            print(f"{donor} {wdt} exit={result}", file=catalog)
    return failures


if __name__ == "__main__":
    sys.exit(main())
