// From jibay442/forever_docker app/macos/keypatch.c (GPL-2.0), plus the Everlake additions marked below.
// Loaded into the re-signed macOS client with DYLD_INSERT_LIBRARIES: writes the server's public key into the
// client's key store, from inside the process (the official app is hardened, so task_for_pid is refused).
//
// Same logic as app/src-tauri/src/patcher/mod.rs: 12 entries of 40 bytes (u32 id 1..12, 32-byte Ed25519 key,
// u8 flag, 3 bytes padding) in a heap array; the entry of the region group (8) gets the server's key.
//
// Environment: FOREVER_SERVER_KEY (64 hex chars, default: TrinityCore), FOREVER_REGION_GROUP (default 8),
// FOREVER_PATCH_LOG (log file, default: stderr), FOREVER_ANY_PORTAL (1: accept a login name of our own, see below).

#include <mach/mach.h>
#include <mach-o/dyld.h>
#include <mach-o/loader.h>
#include <netdb.h>
#include <mach/mach_vm.h>
#include <pthread.h>
#include <stdarg.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <time.h>
#include <unistd.h>

#define ENTRY 40
#define COUNT 12
#define BLOCK (ENTRY * COUNT)
#define CHUNK (8 * 1024 * 1024)

static const char *KNOWN_KEYS[COUNT] = {
    "9B0671C815DFF513BFD4A2B26AE1F84EC9106841B2FB620DB65F6ADE7C21AD06",
    "112451B9843C2799E4750AA3D9B9AFABF536A645B863C4ADB2078B932B354E04",
    "A6B858485748BF38BE193517AB90F8DE169EFF0989EA9360DB346A378B0FFE15",
    "50FFDEB807F8FF73A299A16000AA6575C5945BE875ADFC8795374ED3415249AC",
    "9F842D078755647C008E5FE3E12B839A981DE02A1F520CB2545CC04CD2323E0E",
    "B8CC75864D8EF461D8DD6AA7425E2C63C24D3982066B8D773A1549BFE24E05EE",
    "76BB7BBDD9F34E124A4573C3AA227E3C87CC603506697D054F6FDEC342E56EBF",
    "1FD6DD8FA0EC30D39E3F72E755B8A045BDE0F70449DC71008B767C2EAA89FB9F",
    "15D618BD7DB577BD9A8D45769C59E4FC631633BF447398A4B489B4C26FBC03AD",
    "B34EC592D2AC4993F5FFC85B15C3DA9078694051CB224345592AF7136D796C99",
    "9E91586DD4B115AB0568B21959E181F6545910BC5E2F30B8975CA09D7BC3EFCE",
    "769C824A1DADD19AEEFA420414D1732DAF44537F98AA229FC5477BCA55F9D59A",
};
static const uint8_t EXPECTED_FLAGS[COUNT] = {1, 1, 1, 1, 1, 1, 1, 1, 0, 1, 1, 1};
static const char *STOCK_PUBLIC_KEY = "02596F0D0C061A8B30745988FD72C59E29EC367FB0F341F28E0F08D037BAFC69";

enum state { INVALID, ORIGINAL, PATCHED };

static uint8_t keys[COUNT][32];
static uint8_t new_key[32];
static int group = 8;
static FILE *log_file;
static uint8_t *chunk;

static void say(const char *fmt, ...) {
    time_t t = time(NULL);
    char ts[16];
    strftime(ts, sizeof ts, "%H:%M:%S", localtime(&t));
    FILE *out = log_file ? log_file : stderr;   // the game can look up a host before the constructor ran
    fprintf(out, "%s [forever] ", ts);
    va_list ap;
    va_start(ap, fmt);
    vfprintf(out, fmt, ap);
    va_end(ap);
    fputc('\n', out);
    fflush(out);
}

static int hex32(const char *s, uint8_t out[32]) {
    if (!s || strlen(s) != 64) return 0;
    for (int i = 0; i < 32; i++) {
        unsigned v;
        if (sscanf(s + 2 * i, "%2x", &v) != 1) return 0;
        out[i] = (uint8_t)v;
    }
    return 1;
}

// Copies process memory without faulting if the region goes away meanwhile.
static int read_mem(mach_vm_address_t addr, void *buf, mach_vm_size_t len) {
    mach_vm_size_t got = 0;
    return mach_vm_read_overwrite(mach_task_self(), addr, len, (mach_vm_address_t)buf, &got) == KERN_SUCCESS &&
           got == len;
}

static enum state store_state(mach_vm_address_t array) {
    uint8_t raw[BLOCK];
    if (!read_mem(array, raw, BLOCK)) return INVALID;
    for (int e = 0; e < COUNT; e++) {
        const uint8_t *o = raw + e * ENTRY;
        uint32_t id;
        memcpy(&id, o, 4);
        if (id != (uint32_t)e + 1) return INVALID;
        int ok = memcmp(o + 4, keys[e], 32) == 0 || (e == group - 1 && memcmp(o + 4, new_key, 32) == 0);
        // o[37] is uninitialised padding: not checked
        if (!ok || o[36] != EXPECTED_FLAGS[e] || o[38] || o[39]) return INVALID;
    }
    return memcmp(raw + (group - 1) * ENTRY + 4, new_key, 32) == 0 ? PATCHED : ORIGINAL;
}

// Read/write regions holding a store. Returns the number found (at most `max` are stored in `out`).
static int find_stores(mach_vm_address_t *out, int max) {
    int n = 0;
    mach_vm_address_t addr = 0;
    for (;;) {
        mach_vm_size_t size = 0;
        vm_region_basic_info_data_64_t info;
        mach_msg_type_number_t cnt = VM_REGION_BASIC_INFO_COUNT_64;
        mach_port_t obj;
        if (mach_vm_region(mach_task_self(), &addr, &size, VM_REGION_BASIC_INFO_64, (vm_region_info_t)&info, &cnt,
                           &obj) != KERN_SUCCESS)
            break;
        int rw = (info.protection & (VM_PROT_READ | VM_PROT_WRITE)) == (VM_PROT_READ | VM_PROT_WRITE);
        int ours = addr <= (mach_vm_address_t)chunk && (mach_vm_address_t)chunk < addr + size;
        if (rw && !ours) {
            for (mach_vm_address_t off = addr; off < addr + size;) {
                mach_vm_size_t take = addr + size - off < CHUNK ? addr + size - off : CHUNK;
                if (!read_mem(off, chunk, take)) break;
                for (uint8_t *p = chunk; (p = memmem(p, take - (p - chunk), keys[0], 32)); p++) {
                    mach_vm_address_t hit = off + (p - chunk);
                    if (hit % 4 == 0 && hit >= 4 && store_state(hit - 4) != INVALID) {
                        if (n < max) out[n] = hit - 4;
                        n++;
                    }
                }
                if (take < CHUNK) break;
                off += CHUNK - 32;
            }
        }
        addr += size;
    }
    return n;
}

static int patch(mach_vm_address_t array) {
    uint8_t *target = (uint8_t *)array + (group - 1) * ENTRY + 4;
    for (int i = 0; i < 20; i++) {
        uint8_t cur[32];
        if (read_mem((mach_vm_address_t)target, cur, 32)) {
            if (memcmp(cur, new_key, 32) == 0) return 1;
            if (memcmp(cur, keys[group - 1], 32) == 0) {
                memcpy(target, new_key, 32);
                return store_state(array) == PATCHED;
            }
        }
        usleep(300 * 1000);
    }
    return 0;
}

static void *watch(void *arg) {
    (void)arg;
    mach_vm_address_t store = 0;
    int said_waiting = 0;
    say("loaded (PID %d, group %d)", getpid(), group);
    for (;;) {
        if (store) {
            switch (store_state(store)) {
            case PATCHED:
                sleep(2);
                break;
            case ORIGINAL:
                if (patch(store)) {
                    say("server key put back");
                } else {
                    say("writing the key failed");
                    store = 0;
                }
                break;
            case INVALID:
                say("key store freed, searching again at the next login");
                store = 0;
                break;
            }
            continue;
        }
        mach_vm_address_t found[4];
        int n = find_stores(found, 4);
        if (n == 1) {
            if (patch(found[0])) {
                say("server key written (group %d) at 0x%llx", group, (unsigned long long)found[0]);
                store = found[0];
            } else {
                say("writing the key failed, retrying");
            }
        } else if (n == 0) {
            if (!said_waiting) {
                say("waiting for the key store...");
                said_waiting = 1;
            }
            usleep(500 * 1000);
        } else {
            say("%d key stores at once: waiting", n);
            usleep(500 * 1000);
        }
    }
    return NULL;
}

// Everlake (Test 1, docs/era-data.md): every hostname the game looks up goes to the log, and with FOREVER_BLOCK_HOSTS
// (comma-separated name parts, e.g. "patch.battle.net,version.battle.net") those lookups fail as if offline. The gate2
// test copy runs our own build of 1.60.1.70790; without this the game asks Blizzard's version service, sees a newer
// live build and tries to update to it.
static int blocked_host(const char *name) {
    const char *list = getenv("FOREVER_BLOCK_HOSTS");
    if (!name || !list || !*list) return 0;
    char buf[512];
    strncpy(buf, list, sizeof buf - 1);
    buf[sizeof buf - 1] = 0;
    for (char *tok = strtok(buf, ","); tok; tok = strtok(NULL, ","))
        if (*tok && strstr(name, tok)) return 1;
    return 0;
}

static int everlake_getaddrinfo(const char *node, const char *service, const struct addrinfo *hints,
                                struct addrinfo **res) {
    if (blocked_host(node)) {
        say("dns %s: blocked", node);
        return EAI_NONAME;
    }
    int rc = getaddrinfo(node, service, hints, res);
    say("dns %s:%s -> %s", node ? node : "-", service ? service : "-", rc == 0 ? "ok" : gai_strerror(rc));
    return rc;
}

__attribute__((used)) static struct { const void *replacement; const void *replacee; } everlake_interpose[]
    __attribute__((section("__DATA,__interpose"))) = {
    { (const void *)(unsigned long)&everlake_getaddrinfo, (const void *)(unsigned long)&getaddrinfo },
};

// Everlake: a login name of our own (docs/client-connection.md). The client only logs in at names ending in one of
// Blizzard's domains: a table of {suffix, suffix length, sign-in page} entries, ".actual.battle.net" (18) first. Like
// Arctium's launcher (src/Patterns/Common.cs, Portal), the ending is blanked, so every name ends with it; its length in
// the table is set to 0 as well, in case the check compares that many bytes. Read in the Windows Wow.exe of
// 1.60.1.70790; the Mac build is assumed to hold the same table. Runs in the constructor, before the game reads it.
static const char PORTAL_SUFFIX[] = ".actual.battle.net";

// Writes into the game's own image: read-only pages get a private writable copy, then their protection back.
static int write_image(void *at, const void *src, size_t len) {
    mach_vm_address_t page = (mach_vm_address_t)at & ~((mach_vm_address_t)vm_page_size - 1);
    mach_vm_size_t span = (((mach_vm_address_t)at + len - page) + vm_page_size - 1) & ~((mach_vm_size_t)vm_page_size - 1);
    mach_vm_address_t region = page;
    mach_vm_size_t size = 0;
    vm_region_basic_info_data_64_t info;
    mach_msg_type_number_t cnt = VM_REGION_BASIC_INFO_COUNT_64;
    mach_port_t obj;
    if (mach_vm_region(mach_task_self(), &region, &size, VM_REGION_BASIC_INFO_64, (vm_region_info_t)&info, &cnt, &obj) !=
        KERN_SUCCESS)
        return 0;
    if (mach_vm_protect(mach_task_self(), page, span, FALSE, VM_PROT_READ | VM_PROT_WRITE | VM_PROT_COPY) != KERN_SUCCESS)
        return 0;
    memcpy(at, src, len);
    mach_vm_protect(mach_task_self(), page, span, FALSE, info.protection);
    return 1;
}

static void any_portal(void) {
    const struct mach_header_64 *mh = NULL;
    intptr_t slide = 0;
    for (uint32_t i = 0; i < _dyld_image_count(); i++) {
        const struct mach_header *h = _dyld_get_image_header(i);
        if (h && h->filetype == MH_EXECUTE && h->magic == MH_MAGIC_64) {
            mh = (const struct mach_header_64 *)h;
            slide = _dyld_get_image_vmaddr_slide(i);
            break;
        }
    }
    if (!mh) {
        say("login name: game image not found, not patched");
        return;
    }
    size_t slen = sizeof PORTAL_SUFFIX - 1;
    uint8_t *found[8];
    int nfound = 0, lengths = 0;
    // 1. the ending, as a string of its own (a NUL before and after), in any data section of the game
    const struct load_command *lc = (const struct load_command *)(mh + 1);
    for (uint32_t c = 0; c < mh->ncmds; c++, lc = (const struct load_command *)((const uint8_t *)lc + lc->cmdsize)) {
        if (lc->cmd != LC_SEGMENT_64) continue;
        const struct segment_command_64 *seg = (const struct segment_command_64 *)lc;
        const struct section_64 *sect = (const struct section_64 *)(seg + 1);
        for (uint32_t s = 0; s < seg->nsects; s++, sect++) {
            if (sect->flags & (S_ATTR_PURE_INSTRUCTIONS | S_ATTR_SOME_INSTRUCTIONS)) continue;
            if ((sect->flags & SECTION_TYPE) == S_ZEROFILL) continue;
            uint8_t *base = (uint8_t *)(sect->addr + slide), *end = base + sect->size;
            for (uint8_t *p = base; p + slen < end && (p = memmem(p, end - p, PORTAL_SUFFIX, slen + 1)); p++) {
                if ((p == base || p[-1] == 0) && nfound < 8) found[nfound++] = p;
            }
        }
    }
    if (!nfound) {
        say("login name: \"%s\" not found in the game, not patched", PORTAL_SUFFIX);
        return;
    }
    // 2. table entries pointing at it: {char *suffix; size_t length; ...}, length 18 -> 0
    lc = (const struct load_command *)(mh + 1);
    for (uint32_t c = 0; c < mh->ncmds; c++, lc = (const struct load_command *)((const uint8_t *)lc + lc->cmdsize)) {
        if (lc->cmd != LC_SEGMENT_64) continue;
        const struct segment_command_64 *seg = (const struct segment_command_64 *)lc;
        if (strncmp(seg->segname, "__DATA", 6) != 0 && strncmp(seg->segname, "__AUTH", 6) != 0) continue;
        uint64_t *p = (uint64_t *)(seg->vmaddr + slide), *end = (uint64_t *)(seg->vmaddr + slide + seg->filesize);
        for (; p + 1 < end; p++) {
            for (int f = 0; f < nfound; f++) {
                if (p[0] == (uint64_t)(uintptr_t)found[f] && p[1] == slen) {
                    uint64_t zero = 0;
                    if (write_image(&p[1], &zero, sizeof zero)) lengths++;
                }
            }
        }
    }
    // 3. the ending itself
    int blanked = 0;
    uint8_t zeros[sizeof PORTAL_SUFFIX - 1] = {0};
    for (int f = 0; f < nfound; f++) blanked += write_image(found[f], zeros, slen);
    say("login name: any name allowed (%d of %d \"%s\" blanked, %d table length(s) zeroed)", blanked, nfound,
        PORTAL_SUFFIX, lengths);
}

__attribute__((constructor)) static void start(void) {
    const char *path = getenv("FOREVER_PATCH_LOG");
    log_file = path ? fopen(path, "a") : NULL;
    if (!log_file) log_file = stderr;
    // Child processes (helpers) must not inherit the injection.
    unsetenv("DYLD_INSERT_LIBRARIES");
    const char *any = getenv("FOREVER_ANY_PORTAL");
    if (any && strcmp(any, "1") == 0) any_portal();

    for (int i = 0; i < COUNT; i++) hex32(KNOWN_KEYS[i], keys[i]);
    const char *key = getenv("FOREVER_SERVER_KEY");
    if (!hex32(key ? key : STOCK_PUBLIC_KEY, new_key)) {
        say("invalid server public key");
        return;
    }
    const char *g = getenv("FOREVER_REGION_GROUP");
    if (g) group = atoi(g);
    if (group < 2 || group > 12 || group == 9) {
        say("invalid region group (2-12, except 9)");
        return;
    }
    chunk = malloc(CHUNK);
    pthread_t th;
    if (!chunk || pthread_create(&th, NULL, watch, NULL) != 0) {
        say("could not start");
        return;
    }
    pthread_detach(th);
}
