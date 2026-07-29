package syncengine

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/cache"
)

// streamReopenAttempts caps how many times a FileStream will transparently
// reopen its object after a mid-read failure before giving up and returning
// the error. Two is enough for the case this exists for (a paused audio
// preview holding an idle HTTP body open until the server drops it) without
// masking a genuinely unreachable location behind a retry loop.
const streamReopenAttempts = 2

// FileStream is an open, resumable read of one file at a Location — local or
// remote, since both are rclone fs.Objects. It reads lazily: bytes leave the
// remote only as the consumer asks for them, so a consumer that stops
// reading (an audio preview the user paused, or never played to the end)
// never transfers the rest of the file.
//
// The resumability is what makes an indefinite pause safe. A paused preview
// leaves the underlying HTTP body idle, and cloud backends eventually close
// an idle body; rather than surface that as a playback failure, Read reopens
// the object at the exact byte offset already consumed and carries on. Since
// the offset is byte-exact, the consumer sees one continuous byte stream and
// a decoder mid-frame notices nothing.
//
// Not safe for concurrent use: one consumer, one stream.
type FileStream struct {
	ctx context.Context
	obj fs.Object

	rc     io.ReadCloser
	offset int64
	closed bool
}

// StreamFile opens relPath for lazy reading from the first location in locs
// that actually has it, returning that location alongside the stream. locs
// is tried in the order given — callers pass it already ranked (see
// normalizedLocationOrder in internal/ui: locals before remotes, by
// Priority), so a file that exists on a local drive is never streamed from a
// remote.
//
// A location that doesn't have relPath (or can't be reached at all) is
// skipped; the error is only returned if no location yielded the file.
func StreamFile(ctx context.Context, locs []Location, relPath string) (*FileStream, Location, error) {
	var firstErr error
	for _, loc := range locs {
		f, err := cache.Get(ctx, loc.rcloneSpec())
		if err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("opening %s: %w", loc.Name, err)
			}
			continue
		}
		obj, err := f.NewObject(ctx, relPath)
		if err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("finding %s at %s: %w", relPath, loc.Name, err)
			}
			continue
		}
		return &FileStream{ctx: ctx, obj: obj}, loc, nil
	}
	if firstErr == nil {
		firstErr = fmt.Errorf("no location holds %s", relPath)
	}
	return nil, Location{}, firstErr
}

// Size is the full size of the file being streamed, regardless of how much
// of it has been read.
func (s *FileStream) Size() int64 { return s.obj.Size() }

// Offset is how many bytes have been consumed so far.
func (s *FileStream) Offset() int64 { return s.offset }

func (s *FileStream) open() error {
	rc, err := s.obj.Open(s.ctx, &fs.RangeOption{Start: s.offset, End: -1})
	if err != nil {
		return err
	}
	s.rc = rc
	return nil
}

func (s *FileStream) Read(p []byte) (int, error) {
	if s.closed {
		return 0, io.ErrClosedPipe
	}
	if s.offset >= s.obj.Size() {
		return 0, io.EOF
	}
	for attempt := 0; ; attempt++ {
		if s.rc == nil {
			if err := s.open(); err != nil {
				if attempt >= streamReopenAttempts || s.ctx.Err() != nil {
					return 0, err
				}
				continue
			}
		}
		n, err := s.rc.Read(p)
		s.offset += int64(n)
		switch {
		case err == nil || errors.Is(err, io.EOF):
			// A short/EOF read part-way through the file means the body was
			// truncated (an idle connection dropped, typically) — reopen at
			// the new offset on the next Read rather than reporting the file
			// as finished early.
			if errors.Is(err, io.EOF) && s.offset < s.obj.Size() {
				s.rc.Close()
				s.rc = nil
				if n > 0 {
					return n, nil
				}
				if attempt >= streamReopenAttempts || s.ctx.Err() != nil {
					return 0, err
				}
				continue
			}
			return n, err
		default:
			s.rc.Close()
			s.rc = nil
			if n > 0 {
				// Hand back what was read; the reopen happens on the next call.
				return n, nil
			}
			if attempt >= streamReopenAttempts || s.ctx.Err() != nil {
				return 0, err
			}
		}
	}
}

func (s *FileStream) Close() error {
	s.closed = true
	if s.rc == nil {
		return nil
	}
	rc := s.rc
	s.rc = nil
	return rc.Close()
}
