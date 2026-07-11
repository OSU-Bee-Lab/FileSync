package ui

import "github.com/OSU-Bee-Lab/filesync/internal/syncengine"

// remoteBackendKind is one cloud backend the "Add Location" kind picker
// offers: its dropdown label and the syncengine.BackendType it maps to.
// SignInName is the fuller name used when telling the user which service
// they're about to be sent to sign in to (confirmImportLocation) - it
// defaults to Label when empty, via backendSignInNames.
//
// This is the single source of truth for backend<->label mapping: previously
// screen_remote_wizard.go and screen_location_transfer.go each kept their own
// copy (kindLabels/kindBackends and backendDisplayNames), which could drift.
// Local folders aren't listed here - they're not an rclone backend, and
// callers special-case the leading "Local folder" picker option themselves.
type remoteBackendKind struct {
	Label      string
	SignInName string
	Backend    syncengine.BackendType
}

var remoteBackendKinds = []remoteBackendKind{
	{Label: "SharePoint / OneDrive", Backend: syncengine.BackendOneDrive},
	{Label: "Google Drive", Backend: syncengine.BackendDrive},
	{Label: "Dropbox", Backend: syncengine.BackendDropbox},
	{Label: "S3-compatible", SignInName: "S3-compatible storage", Backend: syncengine.BackendS3},
}

// localFolderLabel is the "Add Location" kind picker's non-backend option.
const localFolderLabel = "Local folder"

// remoteKindLabels returns the labels for the "Add Location" kind picker,
// "Local folder" first followed by every remoteBackendKinds entry in order.
func remoteKindLabels() []string {
	labels := make([]string, 0, len(remoteBackendKinds)+1)
	labels = append(labels, localFolderLabel)
	for _, k := range remoteBackendKinds {
		labels = append(labels, k.Label)
	}
	return labels
}

// remoteKindByLabel maps a kind-picker label back to its BackendType, for
// resolving kindSelect.Selected.
func remoteKindByLabel() map[string]syncengine.BackendType {
	m := make(map[string]syncengine.BackendType, len(remoteBackendKinds))
	for _, k := range remoteBackendKinds {
		m[k.Label] = k.Backend
	}
	return m
}

// backendSignInNames maps each backend to the name used when telling the
// user which service they're about to be sent to sign in to.
func backendSignInNames() map[syncengine.BackendType]string {
	m := make(map[syncengine.BackendType]string, len(remoteBackendKinds))
	for _, k := range remoteBackendKinds {
		name := k.SignInName
		if name == "" {
			name = k.Label
		}
		m[k.Backend] = name
	}
	return m
}
