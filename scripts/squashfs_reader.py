#!/usr/bin/env python3
"""Bounded read-only SquashFS 4.0 reader for pinned AppImage archives.

Only named regular files are read. Nothing in the image is executed, no
AppImage runtime is started and no host tool (unsquashfs, FUSE) is needed.
Symbolic links are resolved only inside the image and only for lookups.
Supported compression: gzip (zlib) and zstd (Python 3.14 compression.zstd).
"""
import struct
import zlib

MAGIC = 0x73717368
METADATA_MAX = 8192
MAX_FILE = 256 * 1024 * 1024
MAX_DIRECTORY = 4 * 1024 * 1024
MAX_LINKS = 16


class SquashError(ValueError):
    pass


def need(condition, code):
    if not condition:
        raise SquashError(code)


class SquashFS:
    def __init__(self, data, offset):
        need(isinstance(offset, int) and 0 <= offset < len(data), "squashfs_offset")
        self.data = memoryview(data)[offset:]
        need(len(self.data) >= 96, "squashfs_truncated")
        fields = struct.unpack_from("<IIIIIHHHHHHQQQQQQQQ", self.data, 0)
        (magic, self.inode_count, _, self.block_size, self.fragment_count, self.compressor,
         block_log, self.flags, _, major, minor, self.root_inode, self.bytes_used, _, _,
         self.inode_table, self.directory_table, self.fragment_table, _) = fields
        need(magic == MAGIC and major == 4 and minor == 0, "squashfs_version")
        need(self.block_size == 1 << block_log and 4096 <= self.block_size <= 1 << 20, "squashfs_block_size")
        need(self.compressor in (1, 6), "squashfs_compressor")
        need(self.bytes_used <= len(self.data), "squashfs_truncated")
        need(self.inode_table < self.directory_table <= self.bytes_used, "squashfs_tables")
        self.data = self.data[:self.bytes_used]
        self._metadata_cache = {}
        self._fragments = None

    def _decompress(self, raw, limit):
        try:
            if self.compressor == 1:
                decoder = zlib.decompressobj()
                result = decoder.decompress(bytes(raw), limit + 1)
                need(decoder.eof and not decoder.unconsumed_tail, "squashfs_compressed")
            else:
                from compression import zstd
                decoder = zstd.ZstdDecompressor()
                result = decoder.decompress(bytes(raw), max_length=limit + 1)
                need(decoder.eof, "squashfs_compressed")
        except (zlib.error, ImportError, Exception) as error:
            if isinstance(error, SquashError):
                raise
            raise SquashError("squashfs_compressed") from None
        need(len(result) <= limit, "squashfs_expanded")
        return result

    def _metadata_block(self, position):
        cached = self._metadata_cache.get(position)
        if cached is not None:
            return cached
        need(0 <= position and position + 2 <= len(self.data), "squashfs_metadata")
        header, = struct.unpack_from("<H", self.data, position)
        size, stored = header & 0x7FFF, bool(header & 0x8000)
        need(0 < size <= METADATA_MAX and position + 2 + size <= len(self.data), "squashfs_metadata")
        raw = self.data[position + 2:position + 2 + size]
        block = bytes(raw) if stored else self._decompress(raw, METADATA_MAX)
        result = (block, position + 2 + size)
        if len(self._metadata_cache) < 4096:
            self._metadata_cache[position] = result
        return result

    def _read_metadata(self, start, offset, length):
        need(0 <= length <= MAX_DIRECTORY, "squashfs_metadata_length")
        output = bytearray()
        position = start
        while len(output) < length:
            block, following = self._metadata_block(position)
            need(offset <= len(block), "squashfs_metadata_offset")
            output.extend(block[offset:])
            offset, position = 0, following
            need(len(output) <= length + METADATA_MAX, "squashfs_metadata_length")
        return bytes(output[:length])

    def _inode(self, reference):
        block, offset = reference >> 16, reference & 0xFFFF
        position = self.inode_table + block
        head = self._read_metadata(position, offset, 16)
        kind, = struct.unpack_from("<H", head, 0)

        def read(size):
            return self._read_metadata(position, offset, 16 + size)[16:]
        if kind == 1:
            block_index, _, file_size, block_offset, _ = struct.unpack("<IIHHI", read(16))
            return {"kind": "dir", "start": block_index, "offset": block_offset, "size": file_size}
        if kind == 8:
            _, file_size, block_index, _, _, block_offset, _ = struct.unpack("<IIIIHHI", read(24))
            return {"kind": "dir", "start": block_index, "offset": block_offset, "size": file_size}
        if kind in (2, 9):
            if kind == 2:
                blocks_start, fragment, fragment_offset, file_size = struct.unpack("<IIII", read(16))
                fixed = 16
            else:
                blocks_start, file_size, _, _, fragment, fragment_offset, _ = struct.unpack("<QQQIIII", read(40))
                fixed = 40
            need(file_size <= MAX_FILE, "squashfs_file_size")
            count = file_size // self.block_size
            if fragment == 0xFFFFFFFF and file_size % self.block_size:
                count += 1
            sizes = struct.unpack("<%dI" % count, read(fixed + 4 * count)[fixed:])
            return {"kind": "file", "start": blocks_start, "size": file_size, "fragment": fragment,
                    "fragment_offset": fragment_offset, "blocks": sizes}
        if kind in (3, 10):
            _, target_size = struct.unpack("<II", read(8))
            need(0 < target_size <= 4096, "squashfs_symlink")
            target = read(8 + target_size)[8:]
            try:
                return {"kind": "symlink", "target": target.decode("utf-8")}
            except UnicodeError:
                raise SquashError("squashfs_symlink") from None
        return {"kind": "other"}

    def _entries(self, inode):
        need(inode["kind"] == "dir", "squashfs_not_directory")
        size = inode["size"]
        need(3 <= size <= MAX_DIRECTORY, "squashfs_directory_size")
        raw = self._read_metadata(self.directory_table + inode["start"], inode["offset"], size - 3)
        position, entries = 0, {}
        while position < len(raw):
            need(position + 12 <= len(raw), "squashfs_directory")
            count, start, _ = struct.unpack_from("<III", raw, position)
            position += 12
            need(count < 256, "squashfs_directory")
            for _ in range(count + 1):
                need(position + 8 <= len(raw), "squashfs_directory")
                offset, _, _, name_size = struct.unpack_from("<HhHH", raw, position)
                position += 8
                name = raw[position:position + name_size + 1]
                position += name_size + 1
                need(len(name) == name_size + 1, "squashfs_directory")
                try:
                    text = name.decode("utf-8")
                except UnicodeError:
                    raise SquashError("squashfs_name") from None
                need(text not in ("", ".", "..") and "/" not in text and "\x00" not in text, "squashfs_name")
                need(text not in entries, "squashfs_duplicate")
                entries[text] = (start << 16) | offset
        return entries

    def lookup(self, path):
        """Resolve a slash-separated path to a regular-file inode."""
        parts = [p for p in path.split("/") if p]
        need(parts and all(p not in (".", "..") for p in parts), "squashfs_path")
        links = 0
        while True:
            inode = self._inode(self.root_inode)
            resolved = []
            restart = None
            for index, part in enumerate(parts):
                entries = self._entries(inode)
                need(part in entries, "squashfs_missing:" + "/".join(parts))
                inode = self._inode(entries[part])
                if inode["kind"] == "symlink":
                    links += 1
                    need(links <= MAX_LINKS, "squashfs_link_loop")
                    target = inode["target"]
                    need(not target.startswith("/"), "squashfs_absolute_link")
                    base = resolved[:]
                    for piece in target.split("/"):
                        if piece in ("", "."):
                            continue
                        if piece == "..":
                            need(base, "squashfs_link_escape")
                            base.pop()
                        else:
                            base.append(piece)
                    restart = base + parts[index + 1:]
                    break
                resolved.append(part)
            if restart is None:
                need(inode["kind"] == "file", "squashfs_not_regular")
                return inode
            parts = restart

    def _fragment(self, index):
        if self._fragments is None:
            count = self.fragment_count
            need(count <= 65536, "squashfs_fragments")
            pointers_size = ((count * 16 + METADATA_MAX - 1) // METADATA_MAX) * 8
            need(self.fragment_table + pointers_size <= len(self.data), "squashfs_fragments")
            pointers = struct.unpack_from("<%dQ" % (pointers_size // 8), self.data, self.fragment_table)
            table = bytearray()
            for pointer in pointers:
                block, _ = self._metadata_block(pointer)
                table.extend(block)
            need(len(table) >= count * 16, "squashfs_fragments")
            self._fragments = [struct.unpack_from("<QII", table, i * 16)[:2] for i in range(count)]
        need(0 <= index < len(self._fragments), "squashfs_fragment_index")
        start, size = self._fragments[index]
        stored, length = bool(size & 0x1000000), size & 0xFFFFFF
        need(0 < length <= self.block_size and start + length <= len(self.data), "squashfs_fragment")
        raw = self.data[start:start + length]
        return bytes(raw) if stored else self._decompress(raw, self.block_size)

    def read(self, path):
        inode = self.lookup(path)
        output = bytearray()
        position = inode["start"]
        for size in inode["blocks"]:
            stored, length = bool(size & 0x1000000), size & 0xFFFFFF
            expected = min(self.block_size, inode["size"] - len(output))
            if length == 0:
                output.extend(bytes(expected))
                continue
            need(position + length <= len(self.data), "squashfs_block")
            raw = self.data[position:position + length]
            position += length
            block = bytes(raw) if stored else self._decompress(raw, self.block_size)
            need(len(block) == expected, "squashfs_block_size")
            output.extend(block)
        if inode["fragment"] != 0xFFFFFFFF:
            fragment = self._fragment(inode["fragment"])
            remaining = inode["size"] - len(output)
            offset = inode["fragment_offset"]
            need(0 < remaining and offset + remaining <= len(fragment), "squashfs_fragment_tail")
            output.extend(fragment[offset:offset + remaining])
        need(len(output) == inode["size"], "squashfs_file_size")
        return bytes(output)

    def list(self, path=""):
        """List one directory (names only); used to register manifest pins."""
        inode = self._inode(self.root_inode)
        for part in [p for p in path.split("/") if p]:
            inode = self._inode(self._entries(inode)[part])
        return {name: self._inode(reference)["kind"] for name, reference in self._entries(inode).items()}
