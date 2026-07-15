package syncengine

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"sync"

	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/config"
	"github.com/rclone/rclone/fs/rc"
	"github.com/rclone/rclone/lib/oauthutil"
)

// BackendType is one of the handful of rclone backends FileSync's curated
// remote wizard exposes. Keep in sync with internal/rcbackends/register.go
// blank imports - a BackendType with no matching registered backend will
// fail at CreateRemote time with a clear rclone error.
type BackendType string

const (
	BackendOneDrive BackendType = "onedrive" // covers SharePoint document libraries too
	BackendDrive    BackendType = "drive"
	BackendDropbox  BackendType = "dropbox"
	BackendS3       BackendType = "s3"
)

// FieldSpec describes one form field the remote wizard must render for a
// backend type.
type FieldSpec struct {
	Key      string
	Label    string
	HelpText string
	IsSecret bool
	Required bool
	Advanced bool
	Default  string
	// Choices holds the backend's suggested values for this field (from
	// rclone's Option.Examples), if any - the wizard renders these as a
	// dropdown instead of free text when non-empty. Not exhaustive: the
	// wizard should still allow typing a custom value for fields like
	// region where rclone ships examples but accepts any string.
	Choices []string
}

// FieldsFor returns the fields the wizard should collect for bt, derived
// directly from rclone's own backend registration so the form never drifts
// out of sync with what the backend actually needs. OAuth token exchange
// itself is handled separately by CreateRemote, not as a field. Advanced
// options are still included (flagged via FieldSpec.Advanced) so the
// wizard can offer them behind a collapsed section rather than hiding them
// outright.
func FieldsFor(bt BackendType) ([]FieldSpec, error) {
	ri, err := fs.Find(string(bt))
	if err != nil {
		return nil, fmt.Errorf("unknown backend %q: %w", bt, err)
	}
	var fields []FieldSpec
	for _, opt := range ri.Options {
		if opt.Hide&fs.OptionHideConfigurator != 0 {
			continue
		}
		var choices []string
		for _, ex := range opt.Examples {
			choices = append(choices, ex.Value)
		}
		fields = append(fields, FieldSpec{
			Key:      opt.Name,
			Label:    opt.Name,
			HelpText: flattenHelp(opt.Help),
			IsSecret: opt.IsPassword,
			Required: opt.Required,
			Advanced: opt.Advanced,
			Default:  fmt.Sprint(opt.GetValue()),
			Choices:  choices,
		})
	}
	return fields, nil
}

// SharePointSiteURLKey is the pseudo-field the OneDrive wizard step uses to
// collect a SharePoint site URL. It isn't an rclone Option - it's consumed
// by CreateRemote to steer the backend's interactive config state machine
// (see stateAnswers below) rather than passed through as a config value.
const SharePointSiteURLKey = "sharepoint_site_url"

// ParseSharePointURL splits whatever SharePoint URL the user pasted into the
// clean site URL rclone needs (scheme://host/sites/Name) and, when the URL
// carried a folder (as it does when copied straight from a document-library
// view in the browser), the path to that folder relative to its library
// root.
//
// It accepts both a bare site URL and a full library view URL like
//
//	https://x.sharepoint.com/sites/OSUBeeLab/Shared%20Documents/Forms/AllItems.aspx?id=%2Fsites%2FOSUBeeLab%2FShared%20Documents%2Faudio_bee_detection%2Fexperiments&...
//
// where the "id" query param is the server-relative path. folderPath is that
// path with the "/sites/Name" prefix and the leading document-library segment
// (e.g. "Shared Documents") stripped, since that segment is the library/drive
// itself rather than a folder within it. folderPath is forward-slash
// separated and may be empty. If raw doesn't look like a SharePoint site URL
// it's returned unchanged with an empty folderPath.
func ParseSharePointURL(raw string) (siteURL, folderPath string) {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return raw, ""
	}
	segs := strings.Split(strings.Trim(u.Path, "/"), "/")
	sitePrefix := ""
	for i, seg := range segs {
		if (seg == "sites" || seg == "teams") && i+1 < len(segs) {
			sitePrefix = "/" + seg + "/" + segs[i+1]
			break
		}
	}
	if sitePrefix == "" {
		// Not a recognizable site URL - hand back a query-stripped URL and
		// let rclone/the user sort it out.
		return u.Scheme + "://" + u.Host + u.Path, ""
	}
	siteURL = u.Scheme + "://" + u.Host + sitePrefix
	if id := u.Query().Get("id"); id != "" {
		// id is the decoded server-relative path, e.g.
		// /sites/OSUBeeLab/Shared Documents/audio_bee_detection/experiments
		p := strings.Trim(strings.TrimPrefix(id, sitePrefix), "/")
		if idx := strings.Index(p, "/"); idx >= 0 {
			folderPath = p[idx+1:] // drop the library segment
		}
	}
	return siteURL, folderPath
}

// DriveInfo identifies one drive/document-library available to an OAuth
// remote - a SharePoint site's libraries (e.g. "Documents") or a personal
// account's OneDrive plus its cache libraries. Type is rclone's drive_type
// ("business", "personal", "documentLibrary"); together ID and Type are all
// that's needed to point a remote (or a one-off connection string) at it.
type DriveInfo struct {
	ID   string
	Name string
	Type string
}

// ChooseDriveFunc is invoked when rclone's config reaches the "which drive?"
// question (option "config_driveid"). It receives every drive the account
// exposes and returns the one to use, or an error to abort (e.g. the user
// cancelled). Only used when creating a remote; see driveConfigSteps.
type ChooseDriveFunc func(drives []DriveInfo) (DriveInfo, error)

// SetRemoteDrive persists a chosen drive onto an existing remote, writing
// only drive_id and drive_type and leaving the token and all other config
// untouched. Used after the user browses to and confirms the right library.
func SetRemoteDrive(remoteName string, d DriveInfo) error {
	if err := config.SetValueAndSave(remoteName, "drive_id", d.ID); err != nil {
		return err
	}
	return config.SetValueAndSave(remoteName, "drive_type", d.Type)
}

// splitDriveLabel pulls the drive name and type out of the label rclone's
// onedrive config builds for each drive, which is always
// fmt.Sprintf("%s (%s)", DriveName, DriveType) - e.g. "Documents
// (documentLibrary)". Falls back to the whole string as the name if the
// parenthesized type isn't there.
func splitDriveLabel(label string) (name, driveType string) {
	i := strings.LastIndex(label, "(")
	j := strings.LastIndex(label, ")")
	if i >= 0 && j > i {
		return strings.TrimSpace(label[:i]), label[i+1 : j]
	}
	return label, ""
}

// flattenHelp joins rclone's (often multi-paragraph) option help text into a
// single line - the wizard renders HelpText as a wrapped hint label, which
// doesn't handle embedded newlines and shows missing-glyph boxes instead.
func flattenHelp(help string) string {
	return strings.Join(strings.Fields(help), " ")
}

// oauthHookMu serializes remote setup: CreateRemote/UpdateRemote briefly
// override the package-level oauthutil.OpenURL hook (see hookOAuthOpenURL),
// which isn't safe for two wizards to do concurrently.
var oauthHookMu sync.Mutex

// CreateRemote drives fs/config.CreateRemote - the same code path rclone's
// own CLI (`rclone config create`) and RC API use, not a CLI-only feature.
// Pre-supplying every field in `fields` lets rclone's config state machine
// skip straight past the per-field questions; for backends that then need
// OAuth (Drive, Dropbox, OneDrive/SharePoint), driveConfigSteps takes over
// from there and onAuthURL is invoked with the sign-in URL as the flow
// reaches it (see hookOAuthOpenURL for how that URL is captured).
func CreateRemote(ctx context.Context, name string, bt BackendType, fields map[string]string, onAuthURL func(url string), chooseDrive ChooseDriveFunc) error {
	oauthHookMu.Lock()
	defer oauthHookMu.Unlock()
	restore := hookOAuthOpenURL(onAuthURL)
	defer restore()

	// The SharePoint site URL isn't a real rclone Option - it steers the
	// onedrive backend's config state machine instead (see stateAnswers).
	siteURL := strings.TrimSpace(fields[SharePointSiteURLKey])
	params := rc.Params{}
	for k, v := range fields {
		if k == SharePointSiteURLKey {
			continue
		}
		params[k] = v
	}
	out, err := config.CreateRemote(ctx, name, string(bt), params, config.UpdateRemoteOpt{
		NonInteractive: true,
		All:            true,
		Obscure:        true,
	})
	if err != nil {
		return err
	}
	// answers are keyed by rclone option name (out.Option.Name), not by state:
	// the onedrive state machine asks "which connection type?" (config_type)
	// and "site URL?" (config_site_url) at states named differently from the
	// answers, so keying by state silently missed them and fell through to the
	// default "onedrive" (personal) flow. Steering config_type to "url" and
	// feeding config_site_url makes it list the SharePoint site's document
	// libraries instead of the signed-in user's personal OneDrive.
	answers := map[string]string{}
	if bt == BackendOneDrive && siteURL != "" {
		answers["config_type"] = "url"
		answers["config_site_url"] = siteURL
	}
	return driveConfigSteps(ctx, name, out, answers, chooseDrive)
}

// UpdateRemote changes fields on an existing remote in place, e.g. to
// refresh credentials or edit a non-secret field.
func UpdateRemote(ctx context.Context, name string, fields map[string]string, onAuthURL func(url string)) error {
	oauthHookMu.Lock()
	defer oauthHookMu.Unlock()
	restore := hookOAuthOpenURL(onAuthURL)
	defer restore()

	params := rc.Params{}
	for k, v := range fields {
		params[k] = v
	}
	out, err := config.UpdateRemote(ctx, name, params, config.UpdateRemoteOpt{
		NonInteractive: true,
		All:            true,
		Obscure:        true,
	})
	if err != nil {
		return err
	}
	return driveConfigSteps(ctx, name, out, nil, nil)
}

// DeleteRemote removes a remote's credentials from rclone's config file.
// Never touches any files on the remote itself.
func DeleteRemote(name string) {
	config.DeleteRemote(name)
}

// ExportedLocation is the portable, secret-free description of a remote
// Location - everything another collaborator needs to recreate the same
// rclone remote on their own machine, minus any credential material.
// Sharing this file does not grant access to anything: the recipient still
// has to sign in (OAuth backends) or supply their own keys (S3) before the
// remote works.
type ExportedLocation struct {
	Name        string            `json:"name"`
	RootPath    string            `json:"rootPath"`
	BackendType BackendType       `json:"backendType"`
	Fields      map[string]string `json:"fields"`
}

// secretRcloneKeys are config keys that never belong in an export even when
// FieldsFor doesn't flag them as IsSecret - chiefly the OAuth token blob,
// which rclone stores under "token" and which FieldsFor never sees because
// oauth fields are hidden from the configurator.
var secretRcloneKeys = map[string]bool{
	"token": true,
}

// ExportLocation captures loc's non-secret rclone remote settings so they
// can be written out for another collaborator to import. Only remote
// locations can be exported - a local Location's RootPath is a path on
// this machine and wouldn't mean anything elsewhere.
func ExportLocation(loc Location) (ExportedLocation, error) {
	if loc.Kind != LocationRemote {
		return ExportedLocation{}, fmt.Errorf("only remote locations can be exported")
	}
	bt, fields, err := nonSecretRemoteFields(loc.RemoteName)
	if err != nil {
		return ExportedLocation{}, err
	}
	return ExportedLocation{
		Name:        loc.Name,
		RootPath:    loc.RootPath,
		BackendType: bt,
		Fields:      fields,
	}, nil
}

// RemoteConfig describes an existing remote's editable state: its backend
// type (so the wizard knows which FieldSpecs to render) and its current
// non-secret field values (so the edit form can be prefilled). Secret
// fields (passwords, keys, OAuth tokens) are deliberately omitted - rclone
// only stores them obscured/encrypted, not in a form worth showing back to
// the user, so the edit form leaves them blank and only overwrites them if
// the user types a new value.
func RemoteConfig(remoteName string) (BackendType, map[string]string, error) {
	return nonSecretRemoteFields(remoteName)
}

// nonSecretRemoteFields looks up remoteName's backend type and every
// non-secret config value rclone has stored for it, filtering out anything
// FieldsFor flags as IsSecret plus the OAuth token blob (see
// secretRcloneKeys). Shared by ExportLocation and RemoteConfig so both stay
// consistent about what counts as safe to surface.
func nonSecretRemoteFields(remoteName string) (BackendType, map[string]string, error) {
	typ, ok := config.FileGetValue(remoteName, "type")
	if !ok {
		return "", nil, fmt.Errorf("remote %q not found in rclone config", remoteName)
	}
	bt := BackendType(typ)
	specs, err := FieldsFor(bt)
	if err != nil {
		return "", nil, err
	}
	secretKeys := map[string]bool{}
	for _, f := range specs {
		if f.IsSecret {
			secretKeys[f.Key] = true
		}
	}
	fields := map[string]string{}
	for _, key := range config.Data().GetKeyList(remoteName) {
		if key == "type" || secretKeys[key] || secretRcloneKeys[key] {
			continue
		}
		if v, ok := config.FileGetValue(remoteName, key); ok {
			fields[key] = v
		}
	}
	return bt, fields, nil
}

// driveConfigSteps advances rclone's non-interactive backend config state
// machine to completion. At each question (out.Option != nil) it uses
// answers[out.Option.Name] if the caller supplied one for that option (e.g.
// steering OneDrive's config_type to "url" and feeding config_site_url for
// SharePoint), otherwise it falls back to the option's own recommended
// default (Option.Default) - for the OAuth confirms this generically means
// "yes, use a local browser" and "yes, refresh the token" (both ship with
// Default: true in oauthutil), which is the right call for a desktop GUI.
// Answers are keyed by option name rather than state name because the state
// a question is reached at (choose_type_done, url_end, ...) differs from the
// intuitive name and is easy to get wrong; the option name (config_type,
// config_site_url) is what the question actually carries.
func driveConfigSteps(ctx context.Context, name string, out *fs.ConfigOut, answers map[string]string, chooseDrive ChooseDriveFunc) error {
	for out != nil && out.State != "" {
		if out.Error != "" {
			return fmt.Errorf("remote config: %s", out.Error)
		}
		result := ""
		if out.Option != nil {
			result = fmt.Sprint(out.Option.GetValue())
			if answer, ok := answers[out.Option.Name]; ok {
				result = answer
			}
		}
		// The SharePoint drive/document-library picker (rclone option
		// "config_driveid") is the one question whose default is meaningless:
		// a site can expose several drives and rclone offers the first as its
		// default, which is often a system library rather than "Documents".
		// So don't auto-default it. On a fresh setup let the user pick from
		// the offered drives (chooseDrive); on a reconnect/edit of an
		// already-configured remote, keep the drive_id already stored rather
		// than silently re-picking the wrong first one.
		if out.Option != nil && out.Option.Name == "config_driveid" {
			if chooseDrive != nil {
				drives := make([]DriveInfo, len(out.Option.Examples))
				for i, ex := range out.Option.Examples {
					dName, dType := splitDriveLabel(ex.Help)
					drives[i] = DriveInfo{ID: ex.Value, Name: dName, Type: dType}
				}
				chosen, err := chooseDrive(drives)
				if err != nil {
					return err
				}
				result = chosen.ID
			} else if existing, ok := config.FileGetValue(name, "drive_id"); ok && existing != "" {
				result = existing
			}
		}
		var err error
		out, err = config.UpdateRemote(ctx, name, rc.Params{}, config.UpdateRemoteOpt{
			NonInteractive: true,
			Continue:       true,
			State:          out.State,
			Result:         result,
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// hookOAuthOpenURL temporarily overrides oauthutil.OpenURL - rclone's own
// extension point for launching a browser during the OAuth loopback flow - so
// the wizard can surface the sign-in URL to onAuthURL instead of a browser
// popping open on its own. It deliberately does NOT call the original (which
// would auto-open the OS browser): the UI shows Open in Browser / Copy Link
// buttons so the user controls when and where sign-in happens. This is the
// exact URL rclone's own `rclone authorize` prints; nothing here
// re-implements OAuth.
//
// Note the URL handed to onAuthURL is rclone's local loopback address
// (http://127.0.0.1:PORT/auth), not the provider's consent URL - rclone's
// local server builds the real Google/Microsoft URL only once the browser
// hits that loopback. That's why we can't inject prompt=select_account to
// force an account picker here: the account shown is whatever the browser is
// already signed into. The UI works around this by offering Copy Link so the
// user can open the loopback URL in a private/incognito window, where the
// provider has no session and shows its login/account chooser.
func hookOAuthOpenURL(onAuthURL func(url string)) (restore func()) {
	original := oauthutil.OpenURL
	oauthutil.OpenURL = func(rawURL string) error {
		if onAuthURL != nil {
			onAuthURL(rawURL)
		}
		return nil
	}
	return func() { oauthutil.OpenURL = original }
}
