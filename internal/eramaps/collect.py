#!/usr/bin/env python3
"""Collect the files a few map tiles need from one build that another build lacks (gate 2, experiment 2).

    python3 tools/tact/collect.py --source <tact cache> --target <tact cache> --wdt FDID --tiles 42-44,11-13
                                  --out <dir>

Reads the map's WDT (FileDataID) from the source build, takes the listed tiles (x range, y range) and follows every
reference: the WDT's own companion files, each tile's root/obj0/obj1/tex0 files, the terrain textures (tex0
MDID/MHID), doodads and buildings (obj0/obj1 MDDF/MODF/MLDD/MLMD, or the MMDX/MWMO names), and inside models
(M2 SFID/TXID/AFID/BFID/PFID/SKID/RPID/GPID) and buildings (WMO GFID groups, MOMT textures, MODI doodads). A file
the target build already has is not followed further: the client uses its own copy.

--only-tiles trims the WDT to the listed tiles (the others marked absent), so the client doesn't look for tiles
that aren't shipped. Writes <dir>/<FileDataID> for every file to add and <dir>/files.tsv (FileDataID, kind, path if known), ready for
`patch.py --files-from <dir>/files.tsv`.
"""
import argparse
import os
import sqlite3
import struct
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import tact  # noqa: E402


def chunks(data, reversed_ids=True):
    """{chunk id: [payloads]} of an IFF-style file (ADT, WDT, WMO, chunked M2)."""
    out, at = {}, 0
    while at + 8 <= len(data):
        cid = data[at:at + 4]
        cid = (cid[::-1] if reversed_ids else cid).decode("latin-1")
        size = struct.unpack_from("<I", data, at + 4)[0]
        out.setdefault(cid, []).append(data[at + 8:at + 8 + size])
        at += 8 + size
    return out


def u32s(blob):
    return list(struct.unpack_from("<%dI" % (len(blob) // 4), blob))


def strings(blob):
    return [s.decode("latin-1") for s in blob.split(b"\0") if s]


class Collector:
    def __init__(self, source, target, listfile, log=print):
        self.src = tact.Tact(source)
        self.target = sqlite3.connect(os.path.join(target, "tact", open(os.path.join(target, "tact", "build.txt"))
                                                   .read().split()[0] + ".sqlite"))
        self.log = log
        self.by_path = {}
        self.path_of = {}
        if listfile and os.path.exists(listfile):
            for line in open(listfile, encoding="utf-8", errors="replace"):
                fid, _, path = line.strip().partition(";")
                if fid.isdigit():
                    self.by_path[path.lower()] = int(fid)
                    self.path_of[int(fid)] = path
        self.wanted = {}      # FileDataID -> kind
        self.seen = set()
        self.in_target = 0

    def in_source(self, fdid):
        with self.src.lock:
            return self.src.db.execute("SELECT 1 FROM files WHERE fdid = ?", (fdid,)).fetchone() is not None

    def has_target(self, fdid):
        return self.target.execute("SELECT 1 FROM files WHERE fdid = ?", (fdid,)).fetchone() is not None

    def name(self, path):
        path = path.lower().replace("\\", "/")
        if path.endswith(".mdx") or path.endswith(".mdl"):
            path = path[:-4] + ".m2"
        return self.by_path.get(path)

    def add(self, fdid, kind):
        """Queue a file. Returns its bytes if it must be followed, else None."""
        if not fdid or fdid in self.seen:
            return None
        self.seen.add(fdid)
        if not self.in_source(fdid):
            return None
        if self.has_target(fdid):
            self.in_target += 1
            return None
        data = self.src.read(fdid)
        self.wanted[fdid] = kind
        os.makedirs(self.out, exist_ok=True)
        open(os.path.join(self.out, str(fdid)), "wb").write(data)
        return data

    # --- formats ---------------------------------------------------------------------------------------------

    def follow(self, fdid, kind):
        data = self.add(fdid, kind)
        if data is None:
            return
        try:
            getattr(self, "follow_" + kind, lambda d: None)(data)
        except (struct.error, UnicodeDecodeError, IndexError) as e:
            self.log("  could not read %s %d (%s): %s" % (kind, fdid, self.path_of.get(fdid, "?"), e))

    def follow_tex0(self, data):
        c = chunks(data)
        for cid in ("MDID", "MHID"):
            for blob in c.get(cid, []):
                for f in u32s(blob):
                    self.follow(f, "blp")
        for blob in c.get("MTEX", []):
            for s in strings(blob):
                self.follow(self.name(s), "blp")

    def _placements(self, c, doodad_chunks, wmo_chunks):
        offs_m = u32s(c["MMID"][0]) if "MMID" in c else []
        offs_w = u32s(c["MWID"][0]) if "MWID" in c else []
        blob_m = c["MMDX"][0] if "MMDX" in c else b""
        blob_w = c["MWMO"][0] if "MWMO" in c else b""

        def by_offset(blob, offs, i):
            if i >= len(offs):
                return None
            end = blob.find(b"\0", offs[i])
            return self.name(blob[offs[i]:end].decode("latin-1"))

        for cid in doodad_chunks:
            for blob in c.get(cid, []):
                for at in range(0, len(blob) - 35, 36):
                    name_id, flags = struct.unpack_from("<I", blob, at)[0], struct.unpack_from("<H", blob, at + 34)[0]
                    self.follow(name_id if flags & 0x40 else by_offset(blob_m, offs_m, name_id), "m2")
        for cid in wmo_chunks:
            # MODF: 64 bytes, flags at 56 (after the extents); MLMD: 40 bytes, flags at 32 (extents are in MLMX)
            size, flag_at = (64, 56) if cid == "MODF" else (40, 32)
            for blob in c.get(cid, []):
                for at in range(0, len(blob) - size + 1, size):
                    name_id = struct.unpack_from("<I", blob, at)[0]
                    flags = struct.unpack_from("<H", blob, at + flag_at)[0]
                    self.follow(name_id if flags & 0x8 else by_offset(blob_w, offs_w, name_id), "wmo")

    def follow_obj0(self, data):
        self._placements(chunks(data), ("MDDF",), ("MODF",))

    def follow_obj1(self, data):
        self._placements(chunks(data), ("MLDD",), ("MLMD",))

    def follow_m2(self, data):
        if data[:4] != b"MD21":
            return
        c = chunks(data, reversed_ids=False)
        for cid in ("SFID", "TXID", "BFID", "PFID", "SKID", "RPID", "GPID"):
            for blob in c.get(cid, []):
                for f in u32s(blob):
                    self.follow(f, "m2" if cid in ("RPID", "GPID") else "file")
        for blob in c.get("AFID", []):
            for at in range(0, len(blob) - 7, 8):
                self.follow(struct.unpack_from("<I", blob, at + 4)[0], "file")

    def follow_wmo(self, data):
        c = chunks(data)
        if "MOHD" not in c:
            return  # a group file: nothing else to follow
        for blob in c.get("GFID", []):
            for f in u32s(blob):
                self.follow(f, "file")
        if "MOTX" in c:
            for s in strings(c["MOTX"][0]):
                self.follow(self.name(s), "blp")
        else:
            for blob in c.get("MOMT", []):
                for at in range(0, len(blob) - 63, 64):
                    values = struct.unpack_from("<16I", blob, at)
                    for i in (3, 6, 9, 12, 13, 14, 15):  # texture 1-3, then extra textures of some shaders
                        if values[i] > 1000 and self.in_source(values[i]):
                            self.follow(values[i], "blp")
        for blob in c.get("MODI", []):
            for f in u32s(blob):
                self.follow(f, "m2")
        for blob in c.get("MOSI", []):
            for f in u32s(blob):
                self.follow(f, "m2")

    # --- the map -----------------------------------------------------------------------------------------

    def collect(self, wdt, xs, ys, out):
        self.out = out
        data = self.add(wdt, "wdt")
        if data is None:
            raise SystemExit("WDT %d: not in the source build, or the target already has it" % wdt)
        c = chunks(data)
        for f in u32s(c["MPHD"][0][4:32]):
            self.follow(f, "file")
        maid = c["MAID"][0]
        for y in ys:
            for x in xs:
                ids = struct.unpack_from("<8I", maid, (y * 64 + x) * 32)
                self.log("tile %d_%d: %s" % (x, y, ids))
                for f, kind in zip(ids, ("file", "obj0", "obj1", "tex0", "file", "blp", "blp", "blp")):
                    self.follow(f, kind)
        with open(os.path.join(out, "files.tsv"), "w") as f:
            for fdid, kind in sorted(self.wanted.items()):
                f.write("%d\t%s\t%s\n" % (fdid, kind, self.path_of.get(fdid, "")))
        return self.wanted


def trim_wdt(data, tiles):
    """The WDT with only `tiles` {(x, y)} left in MAIN and MAID: the others are marked absent and their file ids
    cleared, so the client doesn't look for tiles we don't ship. Chunk sizes stay the same."""
    out, at = bytearray(data), 0
    while at + 8 <= len(out):
        cid = bytes(out[at:at + 4][::-1]).decode("latin-1")
        size = struct.unpack_from("<I", out, at + 4)[0]
        body = at + 8
        if cid in ("MAIN", "MAID"):
            step = size // 4096
            for i in range(4096):
                if (i % 64, i // 64) not in tiles:
                    out[body + i * step:body + (i + 1) * step] = bytes(step)
        at = body + size
    return bytes(out)


def span(text):
    a, _, b = text.partition("-")
    return range(int(a), int(b or a) + 1)


def main():
    ap = argparse.ArgumentParser(description=__doc__.split("\n")[0])
    ap.add_argument("--source", required=True, help="tact cache of the build to take files from")
    ap.add_argument("--target", required=True, help="tact cache of the build the files are added to")
    ap.add_argument("--listfile", default=os.path.expanduser("~/retail-cache-wowf/community-listfile.csv"))
    ap.add_argument("--wdt", type=int, required=True)
    ap.add_argument("--tiles", required=True, help="x range,y range, e.g. 42-44,11-13")
    ap.add_argument("--out", required=True)
    ap.add_argument("--only-tiles", action="store_true",
                    help="trim the WDT to the listed tiles, so the client doesn't look for the map's other tiles")
    args = ap.parse_args()
    xs, ys = (span(p) for p in args.tiles.split(","))
    col = Collector(args.source, args.target, args.listfile)
    wanted = col.collect(args.wdt, xs, ys, args.out)
    if args.only_tiles:
        path = os.path.join(args.out, str(args.wdt))
        trimmed = trim_wdt(open(path, "rb").read(), {(x, y) for x in xs for y in ys})
        open(path, "wb").write(trimmed)
        print("WDT %d trimmed to %d tiles" % (args.wdt, len(xs) * len(ys)))
    kinds = {}
    for k in wanted.values():
        kinds[k] = kinds.get(k, 0) + 1
    size = sum(os.path.getsize(os.path.join(args.out, str(f))) for f in wanted)
    print("%d files to add (%s), %.1f MB; %d referenced files the target already has" % (
        len(wanted), ", ".join("%s %d" % kv for kv in sorted(kinds.items())), size / 1e6, col.in_target))


if __name__ == "__main__":
    main()
