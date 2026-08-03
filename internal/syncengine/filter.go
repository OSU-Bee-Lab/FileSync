package syncengine

import (
	"context"

	"github.com/rclone/rclone/fs/filter"
)

// ExcludeRule is one row of the Settings exclude-patterns list: an rclone
// glob pattern (gitignore-like: a pattern with no "/" matches that name at
// any depth, e.g. ".DS_Store" or "._*") plus whether it's currently active.
// Disabled rules are kept (not dropped) so a user can toggle a default
// junk-file rule back on without retyping it.
type ExcludeRule struct {
	Pattern string `json:"pattern"`
	Enabled bool   `json:"enabled"`
}

// FilterSettings controls which files a copy operation acts on. The zero
// value can be empty, which means copy everything.
type FilterSettings struct {
	// IncludePatterns are rclone glob include patterns, e.g. "*.mp3".
	// A copy with no patterns matches everything.
	IncludePatterns []string `json:"includePatterns"`
	// ExcludeRules are checked before IncludePatterns so an exclude always
	// wins regardless of what's included. Used to keep OS-generated junk
	// files out of scans/copies - see DefaultJunkExcludeRules.
	ExcludeRules []ExcludeRule `json:"excludeRules,omitempty"`
}

// DefaultJunkExcludeRules lists the OS-metadata junk files FileSync filters
// out of scans/copies by default, all enabled. Users can disable or delete
// individual rules (see Settings), but a fresh config starts with these so
// cloud remotes don't accumulate junk that isn't part of the lab's data
// schema.
func DefaultJunkExcludeRules() []ExcludeRule {
	patterns := []string{
		".DS_Store",       // macOS Finder per-folder view state.
		"._*",             // macOS AppleDouble sidecar files, written whenever a file lands on a non-HFS+/APFS filesystem (network shares, exFAT/FAT32 drives, some cloud syncs) that can't hold a resource fork/extended attributes natively.
		".Spotlight-V100", // macOS Spotlight index, sometimes created on external/network volumes.
		".Trashes",        // macOS per-volume trash folder.
		".fseventsd",      // macOS filesystem event log, sometimes created on external volumes.
		".TemporaryItems", // macOS temp-file folder, sometimes created on network volumes.
		"Thumbs.db",       // Windows Explorer thumbnail cache.
		"desktop.ini",     // Windows per-folder view/icon settings.
		"~$*",             // Microsoft Office lock files for an open document.
	}
	rules := make([]ExcludeRule, len(patterns))
	for i, p := range patterns {
		rules[i] = ExcludeRule{Pattern: p, Enabled: true}
	}
	return rules
}

// DefaultFilterSettings does not restrict which files are included, but does
// exclude OS-generated junk by default. FileSync used to default to
// mp3-only, but the lab schema includes other recorder outputs that should
// be scanned and copied unless a future UI explicitly opts into filtering.
func DefaultFilterSettings() FilterSettings {
	return FilterSettings{ExcludeRules: DefaultJunkExcludeRules()}
}

// withFilter returns a context carrying an rclone filter built from fset,
// for use with a single sync/copy call. Never mutates global filter state.
func withFilter(ctx context.Context, fset FilterSettings) (context.Context, error) {
	f, err := filter.NewFilter(nil)
	if err != nil {
		return ctx, err
	}
	// Excludes are added before includes so they always win: rclone's
	// filter rules match in the order they were added and stop at the
	// first match, so a junk-file exclude here can't be overridden by a
	// later include pattern.
	for _, rule := range fset.ExcludeRules {
		if !rule.Enabled {
			continue
		}
		if err := f.Add(false, rule.Pattern); err != nil {
			return ctx, err
		}
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
