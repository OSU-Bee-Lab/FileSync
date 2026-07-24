// Package appversion holds FileSync's build version and checks GitHub
// Releases for a newer one.
package appversion

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Version is FileSync's build version, e.g. "0.3.1". The release workflow
// injects it via -ldflags -X from the git tag being built; local/dev builds
// (`go run .`) leave it at "dev", which CheckForUpdate treats as "don't
// bother checking" since there's no meaningful version to compare.
var Version = "dev"

// repo is the GitHub owner/repo whose releases are checked, matching this
// module's path.
const repo = "OSU-Bee-Lab/filesync"

// Update describes a newer release than the one currently running.
type Update struct {
	Version string // e.g. "0.4.0", without the leading "v"
	URL     string // release page to open in the browser
}

type ghRelease struct {
	TagName string `json:"tag_name"`
	HTMLURL string `json:"html_url"`
	Draft   bool   `json:"draft"`
}

// CheckForUpdate asks GitHub for the latest release and returns it if it's
// newer than Version. It returns (nil, nil) if already up to date, running a
// dev build, or the release couldn't be determined (e.g. offline) - callers
// should treat all of those the same way: no update notice.
func CheckForUpdate(ctx context.Context) (*Update, error) {
	if Version == "dev" {
		return nil, nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"https://api.github.com/repos/"+repo+"/releases/latest", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "FileSync-UpdateCheck")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github releases/latest: unexpected status %s", resp.Status)
	}

	var rel ghRelease
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return nil, err
	}
	if rel.Draft {
		return nil, nil
	}

	latest := strings.TrimPrefix(rel.TagName, "v")
	if compareVersions(latest, Version) <= 0 {
		return nil, nil
	}
	return &Update{Version: latest, URL: rel.HTMLURL}, nil
}

// compareVersions compares two dotted-numeric versions ("1.2.10" vs "1.2.9"),
// returning >0 if a > b, 0 if equal, <0 if a < b. Non-numeric components
// compare as 0, which is good enough for the "vMAJOR.MINOR.PATCH" tags this
// project's release workflow produces.
func compareVersions(a, b string) int {
	as := strings.Split(a, ".")
	bs := strings.Split(b, ".")
	for i := 0; i < len(as) || i < len(bs); i++ {
		var an, bn int
		if i < len(as) {
			an, _ = strconv.Atoi(as[i])
		}
		if i < len(bs) {
			bn, _ = strconv.Atoi(bs[i])
		}
		if an != bn {
			return an - bn
		}
	}
	return 0
}
