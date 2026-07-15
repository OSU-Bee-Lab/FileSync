package drivers

import "strings"

// isFATFamily reports whether fstype names a FAT16/FAT32 filesystem, as
// reported by gopsutil on macOS/Linux/Windows (e.g. "msdos", "vfat",
// "fat32" - naming varies by OS). exFAT is deliberately excluded: general-
// purpose external drives are essentially never FAT32 (the 4GB file-size
// cap makes it impractical for bulk storage) but commonly are exFAT, so
// including exfat here would defeat the filter's purpose of skipping a
// large external HDD's slow Detect() calls. Both currently supported
// recorder models (Sony ICD-PX370, Olympus VN-541PC) report "msdos" on
// macOS and are FAT32-formatted flash storage, so their QuickReject
// implementations use this as a zero-I/O pre-filter. A future driver for
// non-FAT hardware, or one that ships on exFAT-formatted SD cards, is not
// obligated to use this - it can implement its own QuickReject signal, or
// none at all (see DETECTION.md's open question on exFAT SD cards).
func isFATFamily(fstype string) bool {
	switch strings.ToLower(fstype) {
	case "msdos", "vfat", "fat", "fat16", "fat32":
		return true
	default:
		return false
	}
}
