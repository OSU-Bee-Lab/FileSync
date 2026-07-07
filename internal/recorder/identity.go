package recorder

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// TagState is the persisted batch/counter state used to assign new
// recorder IDs, ported from the Olympus tool's recorder_core.py
// (load_state/save_state/proposed_id/bump_number). It's generalized here
// across every Driver, not just Olympus — this tag-file scheme is what
// replaces hub/port position as the source of recorder identity.
//
// Persistence is the caller's responsibility (appconfig owns the on-disk
// config file); TagState is just the in-memory shape callers read and
// mutate.
type TagState struct {
	Batch    int
	Counters map[string]int
}

func (s *TagState) currentNumber() int {
	if s.Counters == nil {
		return 1
	}
	if n, ok := s.Counters[strconv.Itoa(s.Batch)]; ok {
		return n
	}
	return 1
}

func (s *TagState) proposedID() string {
	return fmt.Sprintf("%d_%d", s.Batch, s.currentNumber())
}

func (s *TagState) bumpNumber() {
	if s.Counters == nil {
		s.Counters = make(map[string]int)
	}
	s.Counters[strconv.Itoa(s.Batch)] = s.currentNumber() + 1
}

// AssignOrReadID returns v's persistent recorder ID: the contents of
// d.IDFilePath(v) if it already has one, or a newly-assigned
// "<batch>_<counter>" ID (written to that path and bumping state) if not.
// The returned bool reports whether a new ID was assigned.
func AssignOrReadID(d Driver, v Volume, state *TagState) (id string, isNew bool, err error) {
	idPath := d.IDFilePath(v)

	if data, readErr := os.ReadFile(idPath); readErr == nil {
		if existing := strings.TrimSpace(string(data)); existing != "" {
			return existing, false, nil
		}
	}

	newID := state.proposedID()
	if err := os.WriteFile(idPath, []byte(newID+"\n"), 0o644); err != nil {
		return "", false, err
	}
	state.bumpNumber()
	return newID, true, nil
}
