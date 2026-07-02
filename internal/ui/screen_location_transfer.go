package ui

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/storage"
	"fyne.io/fyne/v2/widget"

	"github.com/OSU-Bee-Lab/expsync/internal/syncengine"
)

// backendDisplayNames mirrors kindBackends in reverse, for messaging the
// user about which service they're about to be sent to sign in to.
var backendDisplayNames = map[syncengine.BackendType]string{
	syncengine.BackendOneDrive: "SharePoint / OneDrive",
	syncengine.BackendDrive:    "Google Drive",
	syncengine.BackendDropbox:  "Dropbox",
	syncengine.BackendS3:       "S3-compatible storage",
}

// exportLocation writes loc's non-secret remote settings to a file the user
// picks, so another collaborator can import it and stand up the same
// remote without any credentials being shared.
func exportLocation(s *state, loc syncengine.Location) {
	exported, err := syncengine.ExportLocation(loc)
	if err != nil {
		dialog.ShowError(err, s.win)
		return
	}
	saveDialog := dialog.NewFileSave(func(uc fyne.URIWriteCloser, err error) {
		if err != nil {
			dialog.ShowError(err, s.win)
			return
		}
		if uc == nil {
			return
		}
		defer uc.Close()
		enc := json.NewEncoder(uc)
		enc.SetIndent("", "  ")
		if err := enc.Encode(exported); err != nil {
			dialog.ShowError(err, s.win)
		}
	}, s.win)
	saveDialog.SetFileName(loc.Name + ".expsync-location.json")
	saveDialog.Show()
}

// importLocation reads a location exported by exportLocation and recreates
// the remote on this machine. Since the file never carries credentials,
// this always requires the importing user to authorize the remote
// themselves - the confirmation dialog and the "Opening your browser..."
// progress step below both exist to make that unavoidable step obvious
// rather than a surprise.
func importLocation(s *state) {
	openDialog := dialog.NewFileOpen(func(uc fyne.URIReadCloser, err error) {
		if err != nil {
			dialog.ShowError(err, s.win)
			return
		}
		if uc == nil {
			return
		}
		defer uc.Close()
		var imported syncengine.ExportedLocation
		if err := json.NewDecoder(uc).Decode(&imported); err != nil {
			dialog.ShowError(fmt.Errorf("couldn't read location file: %w", err), s.win)
			return
		}
		confirmImportLocation(s, imported)
	}, s.win)
	openDialog.SetFilter(storage.NewExtensionFileFilter([]string{".json"}))
	openDialog.Show()
}

func confirmImportLocation(s *state, imported syncengine.ExportedLocation) {
	backendName := backendDisplayNames[imported.BackendType]
	if backendName == "" {
		backendName = string(imported.BackendType)
	}
	msg := fmt.Sprintf("Import \"%s\"?\n\nYou'll be sent to sign in to %s to authorize it - your own account, not the exporter's.", imported.Name, backendName)
	dialog.ShowConfirm("Sign-in required", msg, func(ok bool) {
		if !ok {
			return
		}
		runImportLocation(s, imported)
	}, s.win)
}

func runImportLocation(s *state, imported syncengine.ExportedLocation) {
	remoteName := remoteNameSanitizer.ReplaceAllString(imported.Name, "-")

	progressLabel := widget.NewLabel("Setting up " + imported.Name + "...")
	progressDialog := dialog.NewCustom("Connecting...", "Please wait", progressLabel, s.win)
	progressDialog.Show()

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		err := syncengine.CreateRemote(ctx, remoteName, imported.BackendType, imported.Fields, func(url string) {
			fyne.Do(func() {
				progressLabel.SetText("Opening your browser to sign in...\nIf it doesn't open, visit:\n" + url)
			})
		})
		fyne.Do(func() {
			progressDialog.Hide()
			if err != nil {
				dialog.ShowError(fmt.Errorf("couldn't set up remote: %w", err), s.win)
				return
			}
			s.cfg.Locations = append(s.cfg.Locations, syncengine.Location{
				ID:         newLocationID(),
				Name:       imported.Name,
				Kind:       syncengine.LocationRemote,
				RemoteName: remoteName,
				RootPath:   imported.RootPath,
			})
			s.saveConfig()
			showLocations(s)
		})
	}()
}
