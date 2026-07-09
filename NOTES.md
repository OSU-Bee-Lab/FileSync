# smart-check notes

Goal: extend the smartcopy-style collision check (currently local-only,
`internal/recorder/smartcopy.go`, 5KB prefix compare) to work across synced
locations, including cloud remotes (SharePoint/OneDrive "Teams"), for a
full experiments sync.

## Planned algorithm

1. Build file lists for all locations, keyed by relative path
   (experiment/.../recorder-ID/filename.mp3 — see SCHEMA.md).
2. Compare lists across locations, find colliding paths (matched pairs by
   path, not all-to-all — comparison cost is O(n), not O(n^2)).
3. Where colliding, read first N bytes and compare to determine same vs.
   different recording. **Important:** the two reads may come back
   shorter than N (e.g. a file smaller than N bytes total, or a
   truncated/short-read remote response) and can differ in length from
   each other. A length mismatch is not itself evidence of a different
   recording — compare only over the shared prefix
   (`min(len(a), len(b))` bytes) before concluding same/different.
   Only fall back to filesize as a tiebreaker (step 4) after the shared
   prefix has actually matched.
4. Same + different size -> copy larger to all destinations.
   Same + same size -> no-op.
   Different -> warn user, ask for resolution.

## Performance findings (not format-specific)

- Recorder rollover cap is byte-exact: in the Pollinator Habitat
  experiment, 234/259 rollover-band mp3s (90%) share the exact same byte
  size (1,073,567,316 B) across different recorders/dates. Filesize alone
  cannot distinguish these files — the prefix check must run regardless
  of whether sizes match.
- Remote byte reads are feasible: rclone supports range reads on
  SharePoint/OneDrive/Drive/Dropbox/S3 (`rclone cat --count N`), one
  HTTP round-trip per file, latency- not bandwidth-bound. Cost of a
  remote read is dominated by the round-trip, not N, up to tens/hundreds
  of KB — so increasing N well beyond the minimum needed is cheap for
  remote reads. Local reads are sub-millisecond at any of these sizes
  regardless of N. The Go-side `bytes.Equal` comparison cost is also
  negligible (memcmp-speed) at any N considered here, and since matching
  is done by path (not all-to-all), total comparison work is O(n) in the
  number of colliding files.

## Benchmark: check size vs. sync time (real rclone, `.local/bench-wooster/`)

Deterministic timing experiment against the live `Teams:` remote (not
simulated): 24 real mp3 file pairs sampled from "Luke - Wooster
Strawberry" (45MB-1.07GB range), first 1MiB of each downloaded locally.
Go benchmark (`.local/bench-wooster/bench.go`, gitignored scratch code)
shelled out to real `rclone` for every op, measuring wall-clock per
check size across the same fixed set of pairs.

| check | avg/pair | vs. size-only baseline |
|---|---|---|
| size-only (`rclone lsl`, no byte read) | 2.81s | 1.0x |
| N=5,000 B | 4.50s | 1.6x |
| N=100,000 B | 4.82s | 1.7x |
| N=256,000 B | 4.25s | 1.5x |
| full-file hash (quickxor) | 43.6s | 15.5x |

Confirms the round-trip-bound prediction above with real data: 5KB vs
256KB costs essentially nothing extra (4.25-4.82s, differences are
network jitter, not a byte-count trend) — one remote round trip
regardless of N, at ~1.5-1.7x the cost of a bare size-only stat. The
real expense would be a full-file-hash approach (~10x slower than even
the largest prefix size tested), which is a strong argument for the
capped-prefix approach over hashing whole files. Caveat: the full-file
hash number is extrapolated from measured throughput on the 1MB
partials (17.9 MB/s) scaled to true full file sizes, not directly
measured (full files weren't downloaded); every other number in the
table is a direct wall-clock measurement. Full report:
`.local/bench-wooster/REPORT.md`.

## Documentation (spec research — id3.org, Microsoft ASF spec, WAV/RIFF
refs, RFC 9639, RFC 3533/Vorbis I)

- **MP3 (ID3v2, id3.org v2.3/2.4):** 10-byte fixed header (`"ID3"` +
  version + flags + syncsafe size), size field is 28-bit (max ~256MB
  tag). Audio's first MPEG frame sync begins immediately at
  `10 + tag_size`, no gap. Minimal device tags are tens of bytes-~1KB;
  tags with embedded art can run to MB. Common per-file fields: `TDRC`/
  `TYER` (date), `TRCK` (track number) — device-populated, not
  standardized, positioned as frames within the tag body (i.e. after
  the fixed 10-byte header).
- **WMA (ASF, Microsoft ASF Specification):** Header Object =
  `GUID(16) + Object Size(8) + Number of Header Objects(4) +
  Reserved1(1) + Reserved2(1)` = 30 fixed bytes, then a sequence of
  sub-objects, each itself starting with its own 16-byte GUID + 8-byte
  size. Mandatory sub-objects: File Properties Object, Header Extension
  Object, ≥1 Stream Properties Object; optional: Content Description,
  Extended Content Description, Codec List. Header Object's total size
  is variable (sum of sub-objects), not fixed by spec. The File
  Properties Object contains a per-file File ID (GUID), creation date,
  file size, and packet/duration counts — genuinely per-file unique
  fields — but that object's own GUID (a well-known, fixed value) comes
  first, before its unique payload. Data Object (audio packets) follows
  the Header Object immediately, with its own GUID+size.
- **WAV (RIFF/WAVE, canonical spec + robotplanet.dk/wavinfo on metadata
  chunks):** canonical minimal header is 44 bytes (`RIFF`+size+`WAVE`+
  `fmt `+`data` chunk header), audio samples immediately after. Two
  4-byte fields (RIFF chunk size, data subchunk size) always vary
  per-file since they encode total/audio length — so even a
  "boilerplate" 44-byte WAV header isn't byte-identical across files.
  Pro field recorders (Sound Devices, Zoom, Lectrosonics) commonly add
  `LIST`/`bext`/`iXML` chunks with per-file timestamp/scene metadata
  before `data`, pushing the header larger; no evidence found either
  way for Sony ICD-PX370/Olympus VN-541PC specifically.
- **FLAC (RFC 9639):** starts with 4-byte `fLaC` magic, then mandatory
  STREAMINFO block (38 bytes total: 4-byte block header + 34-byte
  payload) containing sample rate/channels/bit depth/total samples AND
  a 128-bit MD5 of the decoded audio samples. This MD5 makes FLAC files
  with distinct audio content naturally unique within the first ~38
  bytes, without needing tags. Optional blocks after (PADDING,
  VORBIS_COMMENT, PICTURE, SEEKTABLE, CUESHEET) can add many KB before
  audio frames start (PADDING and embedded PICTURE/art are the largest
  common contributors) — irrelevant to collision-distinguishing since
  STREAMINFO's MD5 already suffices.
- **Ogg/Vorbis (RFC 3533, Vorbis I spec):** 27-byte (+segment table)
  Ogg page header per page. Three mandatory Vorbis packets:
  identification header (~30B, fixed-ish), comment header (variable,
  analogous to ID3 — where per-file device metadata would live,
  typically minimal), and setup header (codebooks — spec notes this is
  often several KB, recommends keeping it under ~4KB for streaming).
  Setup header is frequently byte-identical across files from the same
  encoder/device since codebooks are standard/shared — same
  templated-boilerplate risk as WMA's ASF header and MP3's ID3 tag
  structure (though not its content).

## Hands-on verification (bytes actually pulled from our files)

- **MP3:** sample file's ID3v2 tag decoded to ~4096 bytes total
  (10-byte header + ~4086-byte tag body, via syncsafe size field
  `00 00 1F 76`). R analysis (`identical()` on raw-byte prefixes,
  pairwise, all distinct files) on 434 files (mp3/wav/wma) in
  Pollinator Habitat found a 115-125 byte prefix sufficient to give
  zero collisions among all files tested.
- **WMA:** checked two sample files from `[test] Olympus Recorders`
  (24,788 B and 9,324 B). Both start with the standard ASF Header
  Object GUID (`3026B275-8E66-CF11-A6D9-00AA0062CE6C`), header size
  field reads 1998 bytes for both files, and the first 48 bytes are
  byte-identical between the two despite very different total sizes
  and content.
- **WAV:** not yet checked — no WAV sample files available locally.
  Need a real sample from these recorders to confirm which structure
  applies (minimal 44B canonical vs. LIST/bext-augmented) before
  assuming a small N is safe.
- **FLAC / Ogg:** not used by these recorders currently (mp3/wav/wma
  only, per the R analysis above) — spec findings above are for
  reference only, no hands-on verification done or currently needed.

### Does the hands-on data match the documentation?

- **MP3: yes, no contradiction.** ~4096 bytes for a device-generated
  tag is on the larger side of the doc's "tens of bytes to ~1KB
  minimal" range but not surprising or inconsistent — the doc gives a
  rough range, not a hard bound, and device firmware can pad or include
  more frames than a bare-minimum tagger would. The ~120-byte
  distinguishing point lands inside the tag body, consistent with the
  doc's note that fields like `TDRC`/`TRCK` (device-populated,
  per-file) are frame data within the tag, positioned after the fixed
  10-byte header.
- **WMA: yes, and the doc explains why we only saw agreement at 48
  bytes.** Our hands-on only sampled 48 bytes, which per the doc's
  layout is still inside fixed structure: the 30-byte Header Object
  descriptor, plus the first few bytes of the *next* object's own GUID
  (itself a fixed, well-known value, not the per-file File ID). The
  doc says the actual per-file-unique data (File ID GUID, creation
  date, file size) lives inside the File Properties Object's *payload*,
  which starts after that object's own GUID+size prefix — i.e. further
  in than byte 48. So the observed 48-byte match isn't evidence WMA
  needs a much longer prefix than expected; it just means we hadn't
  sampled far enough yet to reach the part the spec says should differ.
  Prediction to verify: bytes should start diverging somewhere in the
  ~48-150 byte range (after the File Properties Object's own ~24-byte
  GUID+size header), well within the ~2000-byte full Header Object we
  already know is safe.

## Open / next steps

- Not locking in a check size N yet. Running the same collision
  analysis across all available files (thousands, not just Pollinator
  Habitat) to see if 115-125 bytes still holds for MP3, or if other
  recorder models/firmware/experiments need a larger N.
- Verify the WMA prediction above: check where in the 48-2000 byte
  range two different WMA files actually start diverging, rather than
  assuming the full ~2000-byte header is required.
- Confirm whether ID3 tag size (~4096 B on the one sampled file) is
  consistent across files/recorders, or varies.
- Get a real WAV sample from these recorders to confirm header
  structure (minimal 44B canonical vs. LIST/bext-augmented) before
  assuming a small N is safe for WAV.
- Decide final N once full-scale collision analysis is done; note that
  going larger than the minimum found is cheap on both local and remote
  reads, so err toward margin rather than the tightest sufficient
  value.
- If FLAC/Ogg are ever adopted, N could differ per-format substantially
  (FLAC's STREAMINFO MD5 gives near-guaranteed uniqueness at ~38 bytes;
  Ogg's setup header may need several KB) — worth handling per-format N
  rather than one fixed N across all extensions, if/when implemented.
