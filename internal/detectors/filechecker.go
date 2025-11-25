package detectors

import "os"

// File is the default reboot checker.
// It is unprivileged, and tests the presence of a files
type File struct {
	FilePath string
}

// Check stats for file presence
// needs refactoring to also return an error, instead of leaking it inside the code.
// This needs refactoring to get rid of NewCommand
// This needs refactoring to only contain file location, instead of CheckCommand
func (rc File) Check() bool {
	if _, err := os.Stat(rc.FilePath); err == nil {
		return true
	}
	return false
}

// NewFileChecker is the constructor for the file based reboot checker
// TODO: Add extra input validation on filePath string here
func NewFileChecker(filePath string) (*File, error) {
	return &File{
		FilePath: filePath,
	}, nil
}
