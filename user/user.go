package user

// Represents a user.
type User struct {
	Username     string `storm:"id"`
	PasswordHash []byte
}
