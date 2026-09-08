package registration_test

import (
	"strings"
	"testing"

	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/registration"
)

const (
	lowercase = "abcdefghijklmnopqrstuvwxyz"
	uppercase = "ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	digits    = "0123456789"
	symbols   = "!#$%&()*+-./:;<=>?@[]^_{|}~"

	// The five punctuation characters a generated password must never contain,
	// and every one of them for a reason a test can state:
	//
	//   ,  the separator of both files it travels through
	//   "  quoting this endpoint has not been shown to honour
	//   '  the quoting an operator is told to use when pasting it
	//   \  escaping that tools disagree about
	//   `  command substitution, if anybody double-quotes it instead
	//
	// Plus whitespace, which no policy needs and every reader mangles.
	forbidden = ",\"'\\`\r\n\t "
)

// Generated once and inspected many times: 200 passwords is enough to catch a
// class that is never emitted or a forbidden character that occasionally is,
// without making the suite slow. One in eight would carry a comma if the
// generator's default symbol set were ever restored, so a handful of samples
// would not be enough to notice.
func passwords(t *testing.T, n int) []string {
	t.Helper()
	out := make([]string, n)
	for i := range out {
		password, err := registration.GeneratePassword()
		if err != nil {
			t.Fatal(err)
		}
		out[i] = password
	}
	return out
}

func TestPasswordLength(t *testing.T) {
	for _, password := range passwords(t, 200) {
		if len(password) != registration.PasswordLength {
			t.Fatalf("password %q is %d characters, want %d",
				password, len(password), registration.PasswordLength)
		}
	}
}

// All four classes in every password, so a tenant's password strength policy
// cannot refuse one - which fails a whole registration rather than one row, and
// is how this requirement was discovered: an alphanumeric password was refused on
// a live tenant.
func TestPasswordHasEveryCharacterClass(t *testing.T) {
	for _, password := range passwords(t, 200) {
		for name, class := range map[string]string{
			"lowercase": lowercase,
			"uppercase": uppercase,
			"digit":     digits,
			"symbol":    symbols,
		} {
			if !strings.ContainsAny(password, class) {
				t.Errorf("password %q has no %s character", password, name)
			}
		}
	}
}

// The constraint that decided the symbol set. This is the test that catches the
// generator's own set being restored: it contains a comma, which would corrupt a
// row of the registration file and of the credentials file - and this endpoint
// reports a row it cannot parse as "Error during password validation.", so the
// corruption would read as a platform fault.
func TestPasswordCarriesNoCharacterTheFilesOrAShellCannot(t *testing.T) {
	for _, password := range passwords(t, 200) {
		if strings.ContainsAny(password, forbidden) {
			t.Fatalf("password %q contains a character the registration file "+
				"or a shell cannot carry", password)
		}
	}
}

func TestPasswordUsesOnlyTheIntendedAlphabet(t *testing.T) {
	pool := lowercase + uppercase + digits + symbols
	for _, password := range passwords(t, 200) {
		for _, r := range password {
			if !strings.ContainsRune(pool, r) {
				t.Fatalf("password %q contains %q, which is outside the pool", password, r)
			}
		}
	}
}

// A password that survives a round trip through the registration file is the
// whole point of restricting the alphabet, so it is asserted through the writer
// rather than by reasoning about characters.
func TestGeneratedPasswordsSurviveTheRegistrationFile(t *testing.T) {
	var records []registration.Record
	for index, password := range passwords(t, 50) {
		records = append(records, registration.Record{
			ExternalID:   "MIGTEST-" + string(rune('A'+index%26)) + string(rune('a'+index/26)),
			ExternalType: "c8y_Serial",
			Name:         "Device",
			Type:         "c8y_MigrationTest",
			Password:     password,
		})
	}

	var out strings.Builder
	if err := registration.WriteRegistrationFile(&out, "", records); err != nil {
		t.Fatalf("a generated password was refused by the writer: %v", err)
	}
	// One line per record plus the header: a password carrying a separator or a
	// quote would show up as a row count that does not match.
	if got, want := strings.Count(strings.TrimSuffix(out.String(), "\n"), "\n")+1, len(records)+1; got != want {
		t.Fatalf("the file has %d lines for %d records and a header", got, want)
	}
	if strings.Contains(out.String(), `"`) {
		t.Errorf("a generated password needed quoting, so it carries a separator:\n%s", out.String())
	}
}

func TestPasswordsAreDistinct(t *testing.T) {
	const samples = 200

	seen := map[string]bool{}
	for _, password := range passwords(t, samples) {
		if seen[password] {
			t.Fatalf("password %q was generated twice in %d draws", password, samples)
		}
		seen[password] = true
	}
}
