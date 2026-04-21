#!/usr/bin/env python3
"""
Extract the CCH attestation seed from a Claude Code CLI binary.

The CLI uses xxhash64(body, seed) & 0xFFFFF to compute the cch= field in
the x-anthropic-billing-header attribution block. The seed is a 64-bit
constant embedded in the binary as an immediate operand (movabs) near a
CALL to one of the xxhash64 functions.

Algorithm:
  1. Locate .text section boundaries from ELF headers
  2. Find all xxhash64 function entries by scanning for PRIME1 constant,
     then walking backwards to the nearest function prologue (push rbp)
  3. Find all CALL E8 instructions targeting those entries
  4. Extract 8-byte movabs immediates within 200 bytes before each CALL
  5. Filter out xxhash's own prime constants
  6. Report candidates (typically ~10); the real seed must be verified
     against a known (body, cch) pair

Usage:
  python3 extract_cch_seed.py /path/to/claude [--verify BODY_FILE]

  --verify BODY_FILE  JSON request body (compact, with cch=XXXXX;) to
                      verify each candidate against. Prints only the
                      matching seed.

Requirements: Python 3.8+, xxhash (pip install xxhash) for --verify mode.
"""

import argparse
import re
import struct
import sys


# xxhash64 algorithm constants — used to identify xxhash code AND to
# exclude from seed candidates.
XXH64_PRIMES = {
    0x9E3779B185EBCA87,  # PRIME1
    0xC2B2AE3D27D4EB4F,  # PRIME2
    0x165667B19E3779F9,  # PRIME3
    0x85EBCA77C2B2AE63,  # PRIME4
    0x27D4EB2F165667C5,  # PRIME5
}

PRIME1_LE = struct.pack("<Q", 0x9E3779B185EBCA87)


def parse_elf_text_section(data: bytes):
    """Return (file_offset, vaddr, size) of the .text section."""
    if data[:4] != b"\x7fELF":
        sys.exit("not an ELF binary")

    e_shoff = struct.unpack_from("<Q", data, 40)[0]
    e_shentsize = struct.unpack_from("<H", data, 58)[0]
    e_shnum = struct.unpack_from("<H", data, 60)[0]
    e_shstrndx = struct.unpack_from("<H", data, 62)[0]

    shstr_off = struct.unpack_from("<Q", data, e_shoff + e_shstrndx * e_shentsize + 24)[0]

    for i in range(e_shnum):
        base = e_shoff + i * e_shentsize
        sh_name = struct.unpack_from("<I", data, base)[0]
        name_end = data.index(b"\x00", shstr_off + sh_name)
        name = data[shstr_off + sh_name : name_end].decode()
        if name == ".text":
            sh_addr = struct.unpack_from("<Q", data, base + 16)[0]
            sh_offset = struct.unpack_from("<Q", data, base + 24)[0]
            sh_size = struct.unpack_from("<Q", data, base + 32)[0]
            return sh_offset, sh_addr, sh_size

    sys.exit(".text section not found")


def find_xxhash_entries(data: bytes, text_file: int, text_vaddr: int, text_size: int):
    """Find function entry points of xxhash64 implementations."""
    text_end = text_file + text_size
    entries = set()

    i = text_file
    while True:
        j = data.find(PRIME1_LE, i, text_end)
        if j < 0:
            break
        for back in range(0, 4096):
            pos = j - back
            if pos < text_file:
                break
            if data[pos] == 0x55 and data[pos + 1 : pos + 4] == b"\x48\x89\xe5":
                entries.add(text_vaddr + (pos - text_file))
                break
        i = j + 1

    return sorted(entries)


def find_callers(data: bytes, text_file: int, text_vaddr: int, text_size: int, entries):
    """Find all CALL E8 sites targeting the given function entries."""
    callers = {}
    entry_set = set(entries)
    text_end = text_file + text_size

    for foff in range(text_file, text_end - 5):
        if data[foff] != 0xE8:
            continue
        disp = struct.unpack_from("<i", data, foff + 1)[0]
        call_vaddr = text_vaddr + (foff - text_file)
        target = call_vaddr + 5 + disp
        if target in entry_set:
            callers.setdefault(target, []).append(call_vaddr)

    return callers


def extract_seed_candidates(data: bytes, text_file: int, text_vaddr: int, callers):
    """Extract 8-byte movabs immediates near each CALL site."""
    seeds = set()

    for _entry, call_sites in callers.items():
        for caller in call_sites:
            cfoff = text_file + (caller - text_vaddr)
            for back in range(0, 200):
                pos = cfoff - back
                if pos < 0:
                    break
                b0, b1 = data[pos], data[pos + 1]
                if b0 in (0x48, 0x49, 0x4C) and 0xB8 <= b1 <= 0xBF:
                    imm = struct.unpack_from("<Q", data, pos + 2)[0]
                    if imm not in XXH64_PRIMES and 0xFFFF < imm < 0xFFFFFFFFFFFFFFFF:
                        seeds.add(imm)

    return sorted(seeds)


def verify_seed(seed: int, body: bytes):
    """Check if xxhash64(body_with_placeholder, seed) & 0xFFFFF matches the cch in body."""
    import xxhash

    m = re.search(rb"cch=([0-9a-fA-F]{5});", body)
    if not m:
        return False
    target = m.group(1).decode().lower()
    ph = re.sub(rb"cch=[0-9a-fA-F]{5};", b"cch=00000;", body)
    h = xxhash.xxh64(ph, seed=seed).intdigest()
    return format(h & 0xFFFFF, "05x") == target


def main():
    parser = argparse.ArgumentParser(description="Extract CCH seed from Claude Code binary")
    parser.add_argument("binary", help="path to the claude CLI binary")
    parser.add_argument("--verify", metavar="BODY_FILE",
                        help="compact JSON body (with cch=XXXXX;) for verification")
    args = parser.parse_args()

    with open(args.binary, "rb") as f:
        data = f.read()

    print(f"binary: {args.binary} ({len(data)} bytes)", file=sys.stderr)

    text_file, text_vaddr, text_size = parse_elf_text_section(data)
    print(f".text: vaddr={text_vaddr:#x} file={text_file:#x} size={text_size:#x}", file=sys.stderr)

    entries = find_xxhash_entries(data, text_file, text_vaddr, text_size)
    print(f"xxhash64 function entries: {len(entries)}", file=sys.stderr)

    callers = find_callers(data, text_file, text_vaddr, text_size, entries)
    total_calls = sum(len(v) for v in callers.values())
    print(f"CALL sites: {total_calls} across {len(callers)} functions", file=sys.stderr)

    seeds = extract_seed_candidates(data, text_file, text_vaddr, callers)
    print(f"seed candidates: {len(seeds)}", file=sys.stderr)

    if args.verify:
        with open(args.verify, "rb") as f:
            body = f.read()
        # strip HTTP headers if present
        sep = body.find(b"\r\n\r\n")
        if sep > 0 and sep < 2000:
            body = body[sep + 4 :]
        matched = 0
        for s in seeds:
            if verify_seed(s, body):
                print(f"{s:#018x}")
                matched += 1
        if matched == 0:
            print("no candidate matched the body", file=sys.stderr)
            sys.exit(1)
    else:
        for s in seeds:
            print(f"{s:#018x}")


if __name__ == "__main__":
    main()
