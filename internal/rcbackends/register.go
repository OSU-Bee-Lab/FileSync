// Package rcbackends registers exactly the rclone backends ExpSync supports.
//
// Deliberately not importing github.com/rclone/rclone/backend/all: that
// package blank-imports all 70+ rclone backends via init(), which bloats
// the binary and slows the build for backends we never expose in the
// remote-setup wizard. Add a backend here (and to the wizard's backend
// picker in internal/ui) if it needs to be supported.
package rcbackends

import (
	_ "github.com/rclone/rclone/backend/drive"    // Google Drive
	_ "github.com/rclone/rclone/backend/dropbox"  // Dropbox
	_ "github.com/rclone/rclone/backend/local"    // local filesystem
	_ "github.com/rclone/rclone/backend/onedrive" // OneDrive / SharePoint document libraries
	_ "github.com/rclone/rclone/backend/s3"       // S3-compatible
)
