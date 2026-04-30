package api

import "golang.org/x/crypto/bcrypt"

// bcryptCompareImpl exists in its own file so the swappable wrapper in
// lockout.go can hide the package-level dependency cleanly without
// importing bcrypt twice.
func bcryptCompareImpl(hash, password string) error {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
}
