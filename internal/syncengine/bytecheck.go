package syncengine

import (
	"bytes"
	"context"
	"io"

	"github.com/rclone/rclone/fs"
)

// PrefixCheckBytes is how many leading bytes of a colliding file we read to
// tell same-content from different-content. See NOTES.md: a 256,000 byte
// prefix clears the metadata headers of every recorder audio format we've
// tested (MP3/ID3v2, WMA/ASF) with wide margin, and remote reads are
// round-trip-latency-bound rather than byte-count-bound, so going well past
// the minimum found in testing costs effectively nothing extra.
//
// Exported so internal/recorder's offload verifier shares the exact same
// prefix length instead of keeping its own. A shorter recorder-side value
// (5000 bytes) once let a genuinely different recording that happened to
// share a byte size and metadata-header prefix be mis-verified as an
// identical copy — which, under auto-delete, could delete the source.
const PrefixCheckBytes int64 = 256000

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
// other (a file smaller than PrefixCheckBytes), only the shared length is
// compared; a length difference alone is not evidence of different content.
//
// The returned reason string is empty unless the action is ActionConflict,
// in which case it's a short human-readable explanation for display
// (e.g. in a conflict-resolution prompt).
func compareObjects(ctx context.Context, srcObj, dstObj fs.Object) (ScanAction, string, error) {
	srcPrefix, err := readPrefix(ctx, srcObj, PrefixCheckBytes)
	if err != nil {
		return ActionConflict, "", err
	}
	dstPrefix, err := readPrefix(ctx, dstObj, PrefixCheckBytes)
	if err != nil {
		return ActionConflict, "", err
	}

	shared := len(srcPrefix)
	if len(dstPrefix) < shared {
		shared = len(dstPrefix)
	}
	prefixMatch := bytes.Equal(srcPrefix[:shared], dstPrefix[:shared])
	sizeMatch := srcObj.Size() == dstObj.Size()

	if sizeMatch && prefixMatch {
		return ActionSkipIdentical, "", nil
	}

	switch {
	case !sizeMatch && prefixMatch:
		return ActionConflict, "different size, same start (possible partial upload)", nil
	case sizeMatch && !prefixMatch:
		return ActionConflict, "same size, different content", nil
	default:
		return ActionConflict, "different size and content", nil
	}
}

// compareObjectsN generalizes compareObjects to more than two present
// copies of the same relative path (see nway.go): every present copy must
// agree with every other on both size and leading bytes for the whole set
// to count as identical. Comparisons are all made against objs[0] — since
// equality is transitive for both a size check and a byte-equality check,
// this is equivalent to checking every pair without the O(n^2) reads.
func compareObjectsN(ctx context.Context, objs []fs.Object) (bool, string, error) {
	if len(objs) < 2 {
		return true, "", nil
	}

	prefixes := make([][]byte, len(objs))
	for i, obj := range objs {
		p, err := readPrefix(ctx, obj, PrefixCheckBytes)
		if err != nil {
			return false, "", err
		}
		prefixes[i] = p
	}

	sizeMismatch := false
	prefixMismatch := false
	for i := 1; i < len(objs); i++ {
		if objs[i].Size() != objs[0].Size() {
			sizeMismatch = true
		}
		shared := len(prefixes[0])
		if len(prefixes[i]) < shared {
			shared = len(prefixes[i])
		}
		if !bytes.Equal(prefixes[0][:shared], prefixes[i][:shared]) {
			prefixMismatch = true
		}
	}

	switch {
	case !sizeMismatch && !prefixMismatch:
		return true, "", nil
	case sizeMismatch && !prefixMismatch:
		return false, "different size, same start (possible partial upload)", nil
	case !sizeMismatch && prefixMismatch:
		return false, "same size, different content", nil
	default:
		return false, "different size and content", nil
	}
}
