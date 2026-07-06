package syncengine

import (
	"context"

	"github.com/rclone/rclone/fs/filter"
)

// FilterSettings controls which files a copy operation acts on. The zero
// value can be empty, which means copy everything.
type FilterSettings struct {
	// IncludePatterns are rclone glob include patterns, e.g. "*.mp3".
	// A copy with no patterns matches everything.
	IncludePatterns []string `json:"includePatterns"`
}

// DefaultFilterSettings does not restrict files. ExpSync used to default to
// mp3-only, but the lab schema includes other recorder outputs that should be
// scanned and copied unless a future UI explicitly opts into filtering.
func DefaultFilterSettings() FilterSettings {
	return FilterSettings{}
}

// withFilter returns a context carrying an rclone filter built from fset,
// for use with a single sync/copy call. Never mutates global filter state.
func withFilter(ctx context.Context, fset FilterSettings) (context.Context, error) {
	f, err := filter.NewFilter(nil)
	if err != nil {
		return ctx, err
	}
	for _, pattern := range fset.IncludePatterns {
		if err := f.Add(true, pattern); err != nil {
			return ctx, err
		}
	}
	if len(fset.IncludePatterns) > 0 {
		// An include-only rule list defaults to "include everything that
		// doesn't match" (rules.include falls through to true), so a
		// trailing exclude-all is required to make this restrictive.
		// Must be "/**", not "*" — "*" doesn't match "/", so it would only
		// exclude top-level names and let non-mp3 files nested under a
		// recorder directory through. "/**" is exactly what rclone's own
		// --include CLI flag appends internally (fs/filter/rules.go
		// parseRules' addImplicitExclude), so this reproduces that
		// behavior precisely rather than approximating it.
		if err := f.Add(false, "/**"); err != nil {
			return ctx, err
		}
	}
	return filter.ReplaceConfig(ctx, f), nil
}
