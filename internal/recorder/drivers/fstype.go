package drivers

import "strings"

// isFATFamily reports whether fstype names a FAT16/FAT32 filesystem, as
// reported by gopsutil on macOS/Linux/Windows (e.g. "msdos", "vfat",
// "fat32" - naming varies by OS). exFAT is deliberately excluded: general-
// purpose external drives are essentially never FAT32 (the 4GB file-size
// cap makes it impractical for bulk storage) but commonly are exFAT, so
// including exfat here would defeat the filter's purpose of skipping a
// large external HDD's slow Detect() calls. Both USB recorder models (Sony
// ICD-PX370, Olympus VN-541PC) report "msdos" on macOS and are
// FAT32-formatted flash storage, so their QuickReject implementations use
// this as a zero-I/O pre-filter. Drivers whose hardware ships on exFAT
// cards - AudioMoth, on anything over 32GB - pair it with isExFAT instead;
// a driver for non-FAT hardware is free to implement its own QuickReject
// signal, or none at all.
func isFATFamily(fstype string) bool {
	switch strings.ToLower(fstype) {
	case "msdos", "vfat", "fat", "fat16", "fat32":
		return true
	default:
		return false
	}
}

// isExFAT reports whether fstype names an exFAT filesystem, as reported by
// gopsutil across platforms. Kept separate from isFATFamily so a driver
// opts into it deliberately: exFAT is the default format for large external
// drives as well as for large SD cards, so accepting it widens QuickReject
// enough that the driver's Detect had better be cheap (see AudioMoth's).
func isExFAT(fstype string) bool {
	switch strings.ToLower(fstype) {
	case "exfat", "exfat-fuse", "fuseblk.exfat":
		return true
	default:
		return false
	}
}
