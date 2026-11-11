"""Stage every WDT-listed terrain file from a pinned Everlake TACT cache."""

import argparse
from concurrent.futures import ThreadPoolExecutor
import hashlib
import json
from pathlib import Path
import struct
import sys


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--tact-tools", type=Path, required=True)
    parser.add_argument("--cache", type=Path, required=True)
    parser.add_argument("--wdt", type=int, required=True)
    parser.add_argument("--out", type=Path, required=True)
    parser.add_argument("--listfile", type=Path, help="collect nested dependencies using this community listfile")
    args = parser.parse_args()

    if not (args.cache / "tact/build.txt").is_file():
        parser.error("initialize the donor TACT cache first")
    sys.path.insert(0, str(args.tact_tools.resolve()))
    from collect import Collector, chunks, u32s
    from tact import Tact

    source = Tact(str(args.cache))
    output = args.out / source.build / str(args.wdt)
    output.mkdir(parents=True, exist_ok=True)

    def fetch(file_id):
        path = output / str(file_id)
        try:
            with source.lock:
                row = source.db.execute(
                    "SELECT ckey FROM files WHERE fdid = ?", (file_id,)
                ).fetchone()
            if row is None:
                raise ValueError("absent from donor build")
            if path.exists() and hashlib.md5(path.read_bytes()).digest() == row[0]:
                return None
            data = source.read(file_id)
            temporary = path.with_suffix(".part")
            temporary.write_bytes(data)
            temporary.replace(path)
            return None
        except Exception as error:
            return str(error)

    def write_inventory(report):
        path = output / "inventory.json"
        temporary = path.with_suffix(".part")
        temporary.write_text(json.dumps(report, indent=2) + "\n", encoding="utf-8")
        temporary.replace(path)

    error = fetch(args.wdt)
    if error:
        write_inventory({
            "version": source.version, "build_config": source.build, "wdt": args.wdt,
            "tiles": {}, "file_ids": [args.wdt], "failures": {args.wdt: error},
        })
        print(f"WDT {args.wdt}: {error}", flush=True)
        return 1
    wdt = (output / str(args.wdt)).read_bytes()
    sections = chunks(wdt)
    main_entries = list(struct.iter_unpack("<2I", sections.get("MAIN", [bytes(4096 * 8)])[0]))
    tile_entries = list(struct.iter_unpack("<8I", sections.get("MAID", [bytes(4096 * 32)])[0]))
    if len(main_entries) != 4096 or len(tile_entries) != 4096:
        parser.error("unexpected WDT tile grid")
    tiles = {
        f"{i % 64},{i // 64}": ids
        for i, ((flags, _), ids) in enumerate(zip(main_entries, tile_entries))
        if flags & 1
    }
    ids = {args.wdt, *u32s(sections["MPHD"][0][4:32])}
    ids.update(file_id for tile in tiles.values() for file_id in tile)
    wdt_dependencies = []
    for section in sections.get("MODF", []):
        for entry in range(0, len(section) - 63, 64):
            if struct.unpack_from("<H", section, entry + 56)[0] & 8:
                wdt_dependencies.append(struct.unpack_from("<I", section, entry)[0])
    ids.update(wdt_dependencies)
    ids.discard(0)
    class Dependencies(Collector):
        def __init__(self):
            self.src = source
            self.by_path = {}
            self.pending = {}
            self.scanned = set()
            self.unresolved_paths = set()
            with args.listfile.open(encoding="utf-8", errors="replace") as lines:
                for line in lines:
                    file_id, _, path = line.strip().partition(";")
                    if file_id.isdecimal() and path:
                        self.by_path[path.lower().replace("\\", "/")] = int(file_id)

        def name(self, path):
            file_id = super().name(path)
            if file_id is None:
                self.unresolved_paths.add(path)
            return file_id

        def follow(self, file_id, kind):
            if file_id and hasattr(self, "follow_" + kind):
                self.pending[file_id, kind] = None
            elif file_id:
                self.pending.setdefault((file_id, "file"), None)

    print(f"{source.version}: WDT {args.wdt}, {len(tiles)} tiles, {len(ids)} files", flush=True)
    failures = {}
    dependencies = None
    with ThreadPoolExecutor(max_workers=24) as pool:
        def fetch_batch(batch):
            ordered = sorted(batch)
            for index, (file_id, error) in enumerate(zip(ordered, pool.map(fetch, ordered)), 1):
                if error:
                    failures[file_id] = error
                if index % 250 == 0:
                    print(f"{index}/{len(ordered)} checked, {len(failures)} failed", flush=True)

        fetch_batch(ids - {args.wdt})
        if args.listfile:
            dependencies = Dependencies()
            for file_id in wdt_dependencies:
                dependencies.follow(file_id, "wmo")
            for tile in tiles.values():
                for file_id, kind in zip(tile[1:4], ("obj0", "obj1", "tex0")):
                    dependencies.follow(file_id, kind)
            while dependencies.pending:
                batch = dependencies.pending.keys() - dependencies.scanned
                dependencies.pending.clear()
                dependencies.scanned.update(batch)
                new_ids = {file_id for file_id, _ in batch} - ids
                fetch_batch(new_ids)
                ids.update(new_ids)
                for file_id, kind in sorted(batch):
                    scanner = getattr(dependencies, "follow_" + kind, None)
                    if scanner and file_id not in failures:
                        try:
                            scanner((output / str(file_id)).read_bytes())
                        except (struct.error, UnicodeDecodeError, IndexError) as error:
                            failures[file_id] = f"dependency scan: {error}"
    report = {
        "version": source.version, "build_config": source.build, "wdt": args.wdt,
        "tiles": tiles, "file_ids": sorted(ids), "failures": failures,
    }
    if dependencies:
        report["unresolved_paths"] = sorted(dependencies.unresolved_paths)
    write_inventory(report)
    print(f"{len(ids) - len(failures)}/{len(ids)} verified in {output}", flush=True)
    return int(bool(failures or (dependencies and dependencies.unresolved_paths)))


if __name__ == "__main__":
    sys.exit(main())
