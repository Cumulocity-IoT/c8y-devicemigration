package registration

import (
	"fmt"

	"github.com/reubenmiller/go-c8y/v2/pkg/password"
)

// PasswordLength is the length of every generated device password.
//
// Fifteen characters over the 89-character pool below is about 97 bits, and sits
// comfortably inside the 8-32 window the bulk registration API accepts.
const PasswordLength = 15

// passwordSymbols is the punctuation a generated password may contain.
//
// A tenant's password strength policy can require a symbol - an alphanumeric
// password was refused on a live tenant with 2026-09-08's stock policy - so
// symbols are not optional. Which ones is decided entirely by where the password
// then has to survive, and there are only two rules:
//
//   - **The registration and credentials files.** Both are comma separated, so a
//     comma is out, and a double quote is out because a value carrying one
//     depends on the reader implementing RFC 4180 quoting, which this endpoint
//     has not been shown to do. CR and LF cannot appear in a line-oriented file
//     at all.
//   - **Pasting the thing.** An operator ends up putting these into curl,
//     mosquitto_pub and basic-auth URLs, and the advice that makes every one of
//     them safe is "wrap it in single quotes". So a single quote is out, since it
//     would end that quoting. A backslash is out because tools disagree about
//     whether it escapes, and a backtick is out because it is the one character
//     that runs a command if anybody ignores the advice and uses double quotes.
//
// That leaves 27 of ASCII's 32 punctuation characters, which is not a meaningful
// loss: the excluded five are the ones that break a file or a shell, not the ones
// that carry entropy.
const passwordSymbols = "!#$%&()*+-./:;<=>?@[]^_{|}~"

// minimumSymbols is how many of them every password must contain.
//
// Two rather than one, matching the generator's own minimums for the other three
// classes, so a policy that demands more than a single symbol is satisfied too.
const minimumSymbols = 2

// passwordOptions configures go-c8y's generator for this endpoint.
//
// Three overrides, and the library's own defaults carry the rest: a minimum of
// two digits, two uppercase and two lowercase characters, which together with
// minimumSymbols is what makes every password acceptable to a tenant on the
// strictest strength setting. A whole registration would otherwise fail on a
// policy nobody looked at first - which is exactly how this came to be tested.
//
// WithCustomSymbols rather than WithSymbols: the generator's own symbol set is
// "`!$@#%^&*()-_=+[]{}|;:,.<>/?~", and it contains a **comma**. Left at the
// default, roughly one password in eight would arrive carrying a field separator
// into a comma-separated file - a corruption that reads as a platform error,
// because this endpoint reports a row it cannot parse as
// "Error during password validation."
func passwordOptions() []password.PasswordOption {
	return []password.PasswordOption{
		// The window the endpoint accepts, and the same guard
		// Devices.Registration.GeneratePassword applies before generating.
		password.WithLengthConstraints(8, 32),
		password.WithLength(PasswordLength),
		password.WithCustomSymbols(passwordSymbols, minimumSymbols),
	}
}

// GeneratePassword returns a fresh device password.
func GeneratePassword() (string, error) {
	generated, err := password.NewRandomPassword(passwordOptions()...)
	if err != nil {
		// Reached only if the options above stop agreeing with each other, or
		// the entropy source fails. Either way a registration must stop rather
		// than write a file of weak or empty passwords.
		return "", fmt.Errorf("cannot generate a device password: %w", err)
	}
	return generated, nil
}
