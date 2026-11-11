#!/usr/bin/env python3
"""Retail files straight from Blizzard's CDN, by file data id (the TACT protocol).

    python3 tact.py --cache <download dir> [--product wow] <file data id> [-o file] [--refresh]

wago.tools serves the same files and is simpler, but it goes down. This asks Blizzard's own
servers, the way the game's launcher does:

  version server   which build is live, and the CDN hosts
  build config     names the build's root and encoding files
  root             file data id -> content key (the MD5 of the file)
  encoding         content key -> encoded key (the name of the stored, compressed file)
  archive indexes  encoded key -> archive, offset, size
  archive          one ranged request for the file, stored as BLTE (zlib chunks, a few encrypted)

The first use of a build downloads the encoding file (190 MB) and 1400 archive indexes (125 MB)
into <cache>/tact and joins them into <cache>/tact/<build config>.sqlite, which takes a few
minutes. After that a file is one lookup and one request. The build stays pinned in
<cache>/tact/build.txt until --refresh, so every file of a patch comes from one build.

Every file is checked against its content key, so a wrong byte anywhere above fails loudly.
"""
import argparse
import concurrent.futures
import hashlib
import os
import sqlite3
import struct
import threading
import time
import urllib.error
import urllib.request
import zlib

VERSIONS = "http://us.patch.battle.net:1119/%s/%s"
KEYS = "https://raw.githubusercontent.com/wowdev/TACTKeys/master/WoW.txt"
UA = {"User-Agent": "wow-mod-retail-creatures/1.0"}
ENUS = 0x2  # root locale flag
LOW_VIOLENCE = 0x80  # root content flag: the censored copy of a file
NO_NAME_HASH = 0x10000000  # root content flag: the block has no name hashes
LOOSE = -1  # archive number of a file stored on its own


class Encrypted(Exception):
    """A file (or part of one) under a key nobody has published: unreleased content."""


def get(url, headers=None, tries=5):
    for attempt in range(tries):
        try:
            with urllib.request.urlopen(urllib.request.Request(url, headers={**UA, **(headers or {})}),
                                        timeout=120) as r:
                return r.read()
        except (urllib.error.URLError, TimeoutError, ConnectionError) as e:
            if attempt == tries - 1 or getattr(e, "code", 0) == 404:
                raise RuntimeError("%s: %s" % (url, e))
            time.sleep(1 + 2 * attempt)


# --- BLTE --------------------------------------------------------------------------------------

def salsa20(key, nonce, data):
    """Salsa20/20 with a 16-byte key and an 8-byte nonce, as TACT uses it."""
    c = struct.unpack("<4I", b"expand 16-byte k")
    k = struct.unpack("<4I", key)
    n = struct.unpack("<2I", nonce)
    mask, stream = 0xFFFFFFFF, bytearray()
    for block in range((len(data) + 63) // 64):
        s = (c[0], k[0], k[1], k[2], k[3], c[1], n[0], n[1], block & mask, block >> 32,
             c[2], k[0], k[1], k[2], k[3], c[3])
        x = list(s)
        for _ in range(10):
            for a, b, e, d in ((0, 4, 8, 12), (5, 9, 13, 1), (10, 14, 2, 6), (15, 3, 7, 11),
                               (0, 1, 2, 3), (5, 6, 7, 4), (10, 11, 8, 9), (15, 12, 13, 14)):
                t = (x[a] + x[d]) & mask
                x[b] ^= (t << 7 | t >> 25) & mask
                t = (x[b] + x[a]) & mask
                x[e] ^= (t << 9 | t >> 23) & mask
                t = (x[e] + x[b]) & mask
                x[d] ^= (t << 13 | t >> 19) & mask
                t = (x[d] + x[e]) & mask
                x[a] ^= (t << 18 | t >> 14) & mask
        stream += struct.pack("<16I", *((x[i] + s[i]) & mask for i in range(16)))
    return bytes(a ^ b for a, b in zip(data, stream))


def blte_chunk(chunk, index, keys):
    mode = chunk[:1]
    if mode == b"N":
        return chunk[1:]
    if mode == b"Z":
        return zlib.decompress(chunk[1:])
    if mode == b"F":
        return blte(chunk[1:], keys)
    if mode == b"E":
        n = chunk[1]
        name = chunk[2:2 + n][::-1].hex().upper()
        iv_size = chunk[2 + n]
        iv = bytearray(chunk[3 + n:3 + n + iv_size].ljust(8, b"\0"))
        kind = chunk[3 + n + iv_size:4 + n + iv_size]
        key = keys.get(name) if keys else None
        if key is None:
            raise Encrypted(name)
        if kind != b"S":
            raise ValueError("BLTE cipher %r" % kind)
        for k in range(4):  # each chunk has its own nonce: the file's, mixed with the chunk number
            iv[k] ^= (index >> (8 * k)) & 0xFF
        return blte_chunk(salsa20(key, bytes(iv), chunk[4 + n + iv_size:]), index, keys)
    raise ValueError("BLTE chunk mode %r" % mode)


def blte(data, keys=None):
    """Decode a stored file. keys: {key name: 16 bytes} for the encrypted chunks."""
    if data[:4] != b"BLTE":
        raise ValueError("not a BLTE file")
    header = struct.unpack_from(">I", data, 4)[0]
    if not header:
        return blte_chunk(data[8:], 0, keys)
    flags, count = data[8], int.from_bytes(data[9:12], "big")
    entry = 40 if flags & 0x10 else 24
    out, at = [], header
    for i in range(count):
        size = struct.unpack_from(">I", data, 12 + i * entry)[0]
        out.append(blte_chunk(data[at:at + size], i, keys))
        at += size
    return b"".join(out)


# --- tables ------------------------------------------------------------------------------------

def table(text):
    """A version server answer: rows of a pipe-separated table as dicts."""
    lines = [l for l in text.splitlines() if l and not l.startswith("#")]
    head = [h.split("!")[0] for h in lines[0].split("|")]
    return [dict(zip(head, l.split("|"))) for l in lines[1:]]


def config(text):
    out = {}
    for line in text.splitlines():
        key, sep, value = line.partition(" = ")
        if sep:
            out[key] = value.split()
    return out


def encoding_entries(data):
    """(content key, encoded key) for every file of the build."""
    if data[:2] != b"EN":
        raise ValueError("not an encoding file")
    _ver, ckey_size, ekey_size, page_kb, _epage_kb, pages, _epages, _unk, espec = struct.unpack_from(
        ">BBBHHIIBI", data, 2)
    at = 22 + espec + pages * (ckey_size + 16)
    for p in range(pages):
        o, end = at + p * page_kb * 1024, at + (p + 1) * page_kb * 1024
        while o + 6 + ckey_size <= end and data[o]:
            count = data[o]
            yield data[o + 6:o + 6 + ckey_size], data[o + 6 + ckey_size:o + 6 + ckey_size + ekey_size]
            o += 6 + ckey_size + count * ekey_size


def index_entries(data):
    """(encoded key, size, offset) of an archive index; a loose file index has offset 0."""
    _ver, _a, _b, block_kb, off_size, size_size, key_size, sum_size, count = struct.unpack_from(
        "<8BI", data, len(data) - 20)
    if key_size != 16 or size_size != 4 or off_size not in (0, 4) or sum_size != 8:
        raise ValueError("unknown archive index layout")
    entry, block = key_size + size_size + off_size, block_kb * 1024
    fmt = ">16sII" if off_size else ">16sI"
    for b in range((len(data) - 28) // (block + key_size + sum_size)):
        usable = block - block % entry
        for row in struct.iter_unpack(fmt, data[b * block:b * block + usable]):
            if not count:
                return
            if row[0] == bytes(16):
                break
            count -= 1
            yield row if off_size else (row[0], row[1], 0)


def root_entries(data):
    """(file data id, content key) for the enUS files, the uncensored copy first."""
    if data[:4] != b"TSFM":
        raise ValueError("not a root file")
    # Since 10.1.7 the header starts with its own size and a version; before, with the counts.
    first, second = struct.unpack_from("<II", data, 4)
    if first < 100:
        at, version = first, second
        total, named = struct.unpack_from("<II", data, 12)
    else:
        at, version, total, named = 12, 1, first, second
    blocks = []
    while at < len(data):
        if version >= 2:
            count, locale, f1, f2, f3 = struct.unpack_from("<IIIIB", data, at)
            flags, at = f1 | f2 | (f3 << 17), at + 17
        else:
            count, flags, locale = struct.unpack_from("<III", data, at)
            at += 12
        blocks.append((flags, locale, count, at))
        at += count * 20
        if not (total != named and flags & NO_NAME_HASH):
            at += count * 8
    if at != len(data):
        raise ValueError("root file layout not understood (version %d)" % version)
    for flags, locale, count, at in sorted(blocks, key=lambda b: bool(b[0] & LOW_VIOLENCE)):
        if not locale & ENUS:
            continue
        fdid = -1
        for i, delta in enumerate(struct.unpack_from("<%di" % count, data, at)):
            fdid += delta + 1
            yield fdid, data[at + count * 4 + i * 16:at + count * 4 + i * 16 + 16]


# --- a build -----------------------------------------------------------------------------------

class Tact:
    def __init__(self, cache, product="wow", refresh=False, log=print):
        self.dir = os.path.join(cache, "tact")
        self.log = log
        os.makedirs(self.dir, exist_ok=True)
        pin = os.path.join(self.dir, "build.txt")
        if refresh or not os.path.exists(pin):
            v = next(r for r in table(get(VERSIONS % (product, "versions")).decode()) if r["Region"] == "us")
            c = next(r for r in table(get(VERSIONS % (product, "cdns")).decode()) if r["Name"] == "us")
            with open(pin, "w") as f:
                f.write("\n".join((v["BuildConfig"], v["CDNConfig"], v["VersionsName"], c["Path"], c["Hosts"])) + "\n")
        self.build, self.cdn, self.version, self.path, hosts = open(pin).read().splitlines()[:5]
        self.hosts = hosts.split()
        self.lock = threading.Lock()
        self.keys = None
        db = os.path.join(self.dir, self.build + ".sqlite")
        if not os.path.exists(db):
            self.index(db)
        self.db = sqlite3.connect(db, check_same_thread=False)
        self.archives = [r[0] for r in self.db.execute("SELECT hash FROM archives ORDER BY id")]

    def url(self, kind, name, suffix=""):
        return "http://%s/%s/%s/%s/%s/%s%s" % (self.hosts[0], self.path, kind, name[:2], name[2:4], name, suffix)

    def stored(self, kind, name, suffix=""):
        """A whole CDN file, kept in the cache."""
        path = os.path.join(self.dir, kind, name + suffix)
        if os.path.exists(path):
            return open(path, "rb").read()
        os.makedirs(os.path.dirname(path), exist_ok=True)
        data = get(self.url(kind, name, suffix))
        tmp = "%s.%d.part" % (path, threading.get_ident())
        with open(tmp, "wb") as f:
            f.write(data)
        os.replace(tmp, path)
        return data

    def index(self, path):
        """Build the lookup database of this build: file data id -> where its bytes are."""
        log = self.log
        log("tact: indexing build %s (once per build, a few minutes)" % self.version)
        build = config(self.stored("config", self.build).decode())
        cdn = config(self.stored("config", self.cdn).decode())
        tmp = path + ".part"
        if os.path.exists(tmp):
            os.remove(tmp)
        db = sqlite3.connect(tmp)
        db.executescript("PRAGMA journal_mode = OFF; PRAGMA synchronous = OFF;"
                         "CREATE TABLE enc (ckey BLOB PRIMARY KEY, ekey BLOB) WITHOUT ROWID;"
                         "CREATE TABLE idx (ekey BLOB PRIMARY KEY, archive INTEGER, offset INTEGER, size INTEGER)"
                         " WITHOUT ROWID;"
                         "CREATE TABLE root (fdid INTEGER PRIMARY KEY, ckey BLOB);"
                         "CREATE TABLE archives (id INTEGER PRIMARY KEY, hash TEXT);"
                         "CREATE TABLE files (fdid INTEGER PRIMARY KEY, ckey BLOB, ekey BLOB, archive INTEGER,"
                         " offset INTEGER, size INTEGER);")
        log("tact: encoding file (%d MB)" % (int(build["encoding-size"][1]) >> 20))
        db.executemany("INSERT OR IGNORE INTO enc VALUES (?, ?)",
                       encoding_entries(blte(self.stored("data", build["encoding"][1]))))
        archives = cdn["archives"]
        log("tact: %d archive indexes" % len(archives))
        db.executemany("INSERT INTO archives VALUES (?, ?)", enumerate(archives))
        with concurrent.futures.ThreadPoolExecutor(12) as ex:
            for n, data in enumerate(ex.map(lambda h: self.stored("data", h, ".index"), archives)):
                db.executemany("INSERT OR IGNORE INTO idx VALUES (?, ?, ?, ?)",
                               ((e, n, off, size) for e, size, off in index_entries(data)))
        for name in cdn.get("file-index", []):
            db.executemany("INSERT OR IGNORE INTO idx VALUES (?, ?, 0, ?)",
                           ((e, LOOSE, size) for e, size, _off in index_entries(self.stored("data", name, ".index"))))
        row = db.execute("SELECT i.ekey, archive, offset, size FROM enc e JOIN idx i ON i.ekey = e.ekey"
                         " WHERE e.ckey = ?", (bytes.fromhex(build["root"][0]),)).fetchone()
        if not row:
            raise RuntimeError("the build's root file is in no archive")
        self.archives = archives
        log("tact: root file")
        db.executemany("INSERT OR IGNORE INTO root VALUES (?, ?)", root_entries(blte(self.raw(*row))))
        db.executescript("INSERT INTO files SELECT r.fdid, r.ckey, i.ekey, i.archive, i.offset, i.size FROM root r"
                         " JOIN enc e ON e.ckey = r.ckey JOIN idx i ON i.ekey = e.ekey;"
                         "DROP TABLE enc; DROP TABLE idx; DROP TABLE root; VACUUM;")
        count = db.execute("SELECT COUNT(*) FROM files").fetchone()[0]
        db.commit()
        db.close()
        os.replace(tmp, path)
        log("tact: %d files indexed" % count)

    def raw(self, ekey, archive, offset, size):
        """The stored (BLTE) bytes of one file."""
        if archive == LOOSE:
            return get(self.url("data", ekey.hex()))
        data = get(self.url("data", self.archives[archive]),
                   {"Range": "bytes=%d-%d" % (offset, offset + size - 1)})
        if len(data) != size:
            raise RuntimeError("archive %s: asked for %d bytes, got %d" % (self.archives[archive], size, len(data)))
        return data

    def read(self, fdid):
        """A file's bytes. KeyError: not in this build. Encrypted: no published key."""
        with self.lock:
            row = self.db.execute("SELECT ckey, ekey, archive, offset, size FROM files WHERE fdid = ?",
                                  (int(fdid),)).fetchone()
        if not row:
            raise KeyError("file %s is not in build %s" % (fdid, self.version))
        raw = self.raw(*row[1:])
        try:
            data = blte(raw)
        except Encrypted:
            with self.lock:
                if self.keys is None:
                    path = os.path.join(self.dir, "keys.txt")
                    if not os.path.exists(path) or time.time() - os.path.getmtime(path) > 86400:
                        open(path, "wb").write(get(KEYS))
                    self.keys = {}
                    for line in open(path).read().split("\n"):
                        parts = line.split()
                        if len(parts) >= 2 and len(parts[1]) == 32:
                            self.keys[parts[0].upper()] = bytes.fromhex(parts[1])
            data = blte(raw, self.keys)
        if hashlib.md5(data).digest() != row[0]:
            raise RuntimeError("file %s doesn't match its content key" % fdid)
        return data


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--cache", required=True)
    ap.add_argument("--refresh", action="store_true", help="move to the build that is live now")
    ap.add_argument("--product", default="wow", help="product to pin a new cache to (wow, wowf, wow_anniversary, ...)")
    ap.add_argument("-o", "--out")
    ap.add_argument("fdid", type=int, nargs="?")
    args = ap.parse_args()
    t = Tact(args.cache, product=args.product, refresh=args.refresh)
    print("build %s (%s)" % (t.version, t.build))
    if args.fdid is not None:
        data = t.read(args.fdid)
        if args.out:
            open(args.out, "wb").write(data)
        print("%d: %d bytes, %s" % (args.fdid, len(data), data[:4]))


if __name__ == "__main__":
    main()
