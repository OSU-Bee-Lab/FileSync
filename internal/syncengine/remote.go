package syncengine

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/config"
	"github.com/rclone/rclone/fs/rc"
	"github.com/rclone/rclone/lib/oauthutil"
)

// BackendType is one of the handful of rclone backends ExpSync's curated
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

// flattenHelp joins rclone's (often multi-paragraph) option help text into a
// single line - the wizard renders HelpText as a Fyne form hint, which
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
func CreateRemote(ctx context.Context, name string, bt BackendType, fields map[string]string, onAuthURL func(url string)) error {
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
	stateAnswers := map[string]string{}
	if bt == BackendOneDrive && siteURL != "" {
		// Without this, driveConfigSteps' default-picking would silently
		// choose "onedrive" (personal/business) and skip the SharePoint
		// site-URL question entirely.
		stateAnswers["choose_type"] = "url"
		stateAnswers["url"] = siteURL
	}
	return driveConfigSteps(ctx, name, out, stateAnswers)
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
	return driveConfigSteps(ctx, name, out, nil)
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
	typ, ok := config.FileGetValue(loc.RemoteName, "type")
	if !ok {
		return ExportedLocation{}, fmt.Errorf("remote %q not found in rclone config", loc.RemoteName)
	}
	bt := BackendType(typ)
	specs, err := FieldsFor(bt)
	if err != nil {
		return ExportedLocation{}, err
	}
	secretKeys := map[string]bool{}
	for _, f := range specs {
		if f.IsSecret {
			secretKeys[f.Key] = true
		}
	}
	fields := map[string]string{}
	for _, key := range config.Data().GetKeyList(loc.RemoteName) {
		if key == "type" || secretKeys[key] || secretRcloneKeys[key] {
			continue
		}
		if v, ok := config.FileGetValue(loc.RemoteName, key); ok {
			fields[key] = v
		}
	}
	return ExportedLocation{
		Name:        loc.Name,
		RootPath:    loc.RootPath,
		BackendType: bt,
		Fields:      fields,
	}, nil
}

// RemoteInfo is one remote already present in rclone's config file,
// whether or not ExpSync created it - lets a power user adopt an existing
// remote (e.g. one set up with the real rclone CLI) into a Location
// instead of only ever creating new ones.
type RemoteInfo struct {
	Name string
	Type string
}

// ListExistingRemotes enumerates every remote in rclone's config file.
func ListExistingRemotes() []RemoteInfo {
	var out []RemoteInfo
	for _, name := range config.FileSections() {
		typ, _ := config.FileGetValue(name, "type")
		out = append(out, RemoteInfo{Name: name, Type: typ})
	}
	return out
}

// driveConfigSteps advances rclone's non-interactive backend config state
// machine to completion. At each question (out.Option != nil) it picks
// stateAnswers[out.State] if the caller supplied one for that state (e.g.
// steering OneDrive's "choose_type" question to "url" for SharePoint),
// otherwise it falls back to the option's own recommended default
// (Option.Default) - for the OAuth confirms this generically means "yes,
// use a local browser" and "yes, refresh the token" (both ship with
// Default: true in oauthutil), which is the right call for a desktop GUI
// and stays correct even if rclone renames its internal states in a future
// version.
func driveConfigSteps(ctx context.Context, name string, out *fs.ConfigOut, stateAnswers map[string]string) error {
	for out != nil && out.State != "" {
		if out.Error != "" {
			return fmt.Errorf("remote config: %s", out.Error)
		}
		result := ""
		if out.Option != nil {
			result = fmt.Sprint(out.Option.GetValue())
		}
		if answer, ok := stateAnswers[out.State]; ok {
			result = answer
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
// extension point for launching a browser during the OAuth loopback flow -
// so the wizard can surface the sign-in URL to onAuthURL (e.g. to show a
// "waiting for sign-in..." screen) before falling back to the original
// (which actually opens the OS browser). This is the exact URL rclone's
// own `rclone authorize` prints/opens; nothing here re-implements OAuth.
func hookOAuthOpenURL(onAuthURL func(url string)) (restore func()) {
	original := oauthutil.OpenURL
	oauthutil.OpenURL = func(url string) error {
		if onAuthURL != nil {
			onAuthURL(url)
		}
		return original(url)
	}
	return func() { oauthutil.OpenURL = original }
}
