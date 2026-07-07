package recorder

import (
	"fmt"
	"os"
	"strings"
)

// readIDFile returns the ID tag stored at idPath. This app never writes a
// recorder ID — assignment is done by an out-of-band process — so a
// missing or empty tag file is an error, not something to fill in.
func readIDFile(idPath string) (string, error) {
	data, err := os.ReadFile(idPath)
	if err != nil {
		return "", fmt.Errorf("no recorder ID found at %s: %w", idPath, err)
	}
	id := strings.TrimSpace(string(data))
	if id == "" {
		return "", fmt.Errorf("recorder ID file %s is empty", idPath)
	}
	return id, nil
}
