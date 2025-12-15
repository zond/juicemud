package game

import "regexp"

var (
	// validUsernameRE matches valid usernames: 1-16 chars, starts with letter,
	// contains only letters, numbers, hyphens, or underscores.
	validUsernameRE = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_-]{0,15}$`)
)

// InvalidUsernameError is returned when a username fails validation.
type InvalidUsernameError struct{}

func (e InvalidUsernameError) Error() string {
	return "Invalid username. Must be 1-16 characters, start with a letter, and contain only letters, numbers, hyphens, or underscores."
}

// validateUsername checks if a username is valid.
// Returns nil if valid, or an InvalidUsernameError describing the problem.
func validateUsername(name string) error {
	if !validUsernameRE.MatchString(name) {
		return InvalidUsernameError{}
	}
	return nil
}
