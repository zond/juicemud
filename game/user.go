package game

// Represents a user.
type User struct {
	Username     string `badgerhold:"key"`
	PasswordHash []byte
	ObjectID     uint64
}
