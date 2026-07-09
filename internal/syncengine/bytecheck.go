package syncengine

import (
	"bytes"
	"context"
	"io"

	"github.com/rclone/rclone/fs"
)

// prefixCheckBytes is how many leading bytes of a colliding file we read to
// tell same-content from different-content. See NOTES.md: a 256,000 byte
// prefix clears the metadata headers of every recorder audio format we've
// tested (MP3/ID3v2, WMA/ASF) with wide margin, and remote reads are
// round-trip-latency-bound rather than byte-count-bound, so going well past
// the minimum found in testing costs effectively nothing extra.
const prefixCheckBytes int64 = 256000

// readPrefix reads up to n leading bytes of obj (local or remote — both are
// rclone fs.Object, so this works uniformly via Object.Open + RangeOption,
// one round trip for remote objects). It tolerates the object being shorter
// than n, returning whatever was actually read.
func readPrefix(ctx context.Context, obj fs.Object, n int64) ([]byte, error) {
	if n > obj.Size() {
		n = obj.Size()
	}
	if n <= 0 {
		return nil, nil
	}

	rc, err := obj.Open(ctx, &fs.RangeOption{Start: 0, End: n - 1})
	if err != nil {
		return nil, err
	}
	defer rc.Close()

	buf := make([]byte, n)
	read, err := io.ReadFull(rc, buf)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return nil, err
	}
	return buf[:read], nil
}

// compareObjects is FileSync's own same-vs-different check for a colliding
// path (src and dst both exist at this relative path), replacing rclone's
// own Equal check (size/modtime/checksum) with a direct file size + leading
// -bytes comparison. See NOTES.md for why: filesize alone can't distinguish
// these recorders' files (rollover hits an exact byte-for-byte cap), and a
// short prefix landing inside a templated metadata header can coincidentally
// match across genuinely different recordings.
//
// Only an exact size match AND an exact shared-prefix match counts as
// identical (ActionSkipIdentical). Anything else — including a size
// mismatch with a matching prefix, which looks like a partial upload of the
// same recording — comes back as ActionConflict so the user confirms it
// rather than the app guessing. When one prefix read is shorter than the
// other (a file smaller than prefixCheckBytes), only the shared length is
// compared; a length difference alone is not evidence of different content.
func compareObjects(ctx context.Context, srcObj, dstObj fs.Object) (ScanAction, error) {
	srcPrefix, err := readPrefix(ctx, srcObj, prefixCheckBytes)
	if err != nil {
		return ActionConflict, err
	}
	dstPrefix, err := readPrefix(ctx, dstObj, prefixCheckBytes)
	if err != nil {
		return ActionConflict, err
	}

	shared := len(srcPrefix)
	if len(dstPrefix) < shared {
		shared = len(dstPrefix)
	}
	prefixMatch := bytes.Equal(srcPrefix[:shared], dstPrefix[:shared])

	if srcObj.Size() == dstObj.Size() && prefixMatch {
		return ActionSkipIdentical, nil
	}
	return ActionConflict, nil
}
