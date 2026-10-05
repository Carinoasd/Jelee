// Package subtitles holds byte-level helpers for external text subtitles
// (G15.4): character set detection and a streaming UTF-8 conversion.
//
// Delivery policy. The original subtitle bytes are what clients receive by
// default (G10.9, G15.5); detection only records the charset as metadata
// (G15.2). A UTF-8 copy is a derived artifact, produced on explicit demand
// into a rebuildable cache that is separate from the media library and may be
// wiped at any time. Nothing in this package opens, rewrites or renames the
// original file; callers hand in readers and writers.
//
// Detection order (DetectCharset):
//  1. Byte order mark: UTF-8, UTF-16LE, UTF-16BE (UTF-32 marks are refused).
//  2. BOM-less UTF-16, recognised by the zero-byte pattern of the ASCII time
//     codes and markup every text subtitle carries.
//  3. A NUL byte anywhere else means binary data (VobSub, PGS) and fails with
//     ErrNotText.
//  4. Streaming UTF-8 validation of every scanned byte. Pure ASCII and valid
//     UTF-8 are reported as UTF-8. Mostly valid UTF-8 with a few broken
//     sequences is still UTF-8, but marked low confidence.
//  5. Otherwise the non-ASCII lines collected while scanning are decoded with
//     each legacy candidate (GB18030, Big5, Shift_JIS, EUC-JP, EUC-KR,
//     windows-1252) and scored: characters common in that candidate's
//     language score high, plausible but rare characters score low, and
//     replacement, private-use and control characters are penalised. For
//     windows-1252, accented letters only count when they sit in short runs
//     next to ASCII letters, which is how Western text looks and CJK text
//     never does.
//
// Memory stays bounded: at most limit bytes are scanned, and only the first
// few KiB plus up to 64 KiB of non-ASCII lines are retained.
//
// Conversion (ToUTF8) streams through golang.org/x/text decoders with an
// input size cap. Invalid sequences become U+FFFD and are counted. One
// leading byte order mark (in any source charset) is removed and no BOM is
// written; interior U+FEFF characters are kept. Line endings are not touched,
// so CRLF stays CRLF.
//
// Derived cache keys (DerivedUTF8Key) are content-addressed hashes over the
// source identity (library-relative path, size, modification time and an
// optional content digest), the source charset and the converter version.
// Changing the source, overriding the charset or bumping ConverterVersion
// yields a new key, so stale entries are never served and can be collected
// by age. Writers should produce the file under a temporary name and rename
// it into place only after ToUTF8 returns nil, because a failed conversion
// may already have written a prefix.
package subtitles
