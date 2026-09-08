package migrate

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/reubenmiller/go-c8y/v2/pkg/c8y/api"

	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/confirm"
	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/export"
	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/importer"
	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/registration"
	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/session"
)

// Options is the resolved form of the command line.
type Options struct {
	RootID string

	MappingFile string

	ChildTypes []export.ChildType
	DataTypes  []export.DataType
	Windows    map[export.DataType]export.Window

	IgnoreLimits bool

	// Incremental moves the hierarchy first and then the data one device at a
	// time, so the staging directory only ever holds a single device's data.
	Incremental bool

	// Resume continues the run with this id instead of starting a new one.
	// Implies Incremental, and needs the journal an incremental run writes.
	Resume string

	// NoFlag leaves created objects unmarked.
	NoFlag bool

	// KeepStaging keeps the staging directory instead of removing it.
	KeepStaging bool

	// Force skips the confirmation.
	Force bool

	// Credentials provisions a device user for every migrated device, by
	// registering them in the target tenant BEFORE the import runs.
	//
	// The order is what makes the credential usable: registration creates the
	// managed object owned by the device user it creates alongside it, and the
	// import then fills that object in. Registering after an import cannot give
	// the device ownership of an object that already exists.
	Credentials bool

	// CredentialsFile is where the credentials are written, the moment
	// registration returns.
	//
	// Optional: empty means the credentials are only available through
	// Credentials(), which is what the microservice uses - it holds them in
	// memory keyed by the job and hands them over once, rather than writing a
	// list of device passwords into a container's filesystem.
	CredentialsFile string

	// IdentityType overrides which external identity a device is registered
	// under. Empty means registration.DefaultIdentityType.
	IdentityType string
}

// Migrator runs one migration.
type Migrator struct {
	source, target *api.Client
	sourceSession  *session.Session
	targetSession  *session.Session

	stderr io.Writer
	stdin  io.Reader
	opts   Options

	journal *Journal
	runID   string

	// markerRunID is the run id actually stamped onto objects, which NoFlag
	// leaves empty. Distinct from runID, which always has a value because it is
	// also how a run is resumed.
	markerRunID string

	stagingDirectory string

	// importStarted distinguishes "this attempt created nothing" from "stopped
	// part way through". Without it a failure before the first import would
	// report an earlier attempt's objects as this one's leftovers.
	importStarted bool

	// credentialsWritten is set once the file exists, so onExit can point at it
	// on a failure. A run that registered devices and then failed has left
	// working credentials in the target, and the operator needs to know the file
	// holding them is the only copy.
	credentialsWritten bool

	// registeredCount is how many devices got a credential.
	registeredCount int

	// credentials is what registration issued, kept for a caller that collects
	// them itself rather than through a file.
	credentials []registration.Credential
}

// Credentials returns what this run issued, in the order they were registered.
//
// Empty unless Options.Credentials was set. The passwords are in this slice and
// nowhere else unless CredentialsFile was given, so a caller that wants them has
// to take them before the Migrator is discarded.
func (m *Migrator) Credentials() []registration.Credential {
	return append([]registration.Credential(nil), m.credentials...)
}

func New(source, target *api.Client, sourceSession, targetSession *session.Session,
	stderr io.Writer, stdin io.Reader, opts Options) *Migrator {

	return &Migrator{
		source: source, target: target,
		sourceSession: sourceSession, targetSession: targetSession,
		stderr: stderr, stdin: stdin, opts: opts,
		journal: NewJournal(opts.MappingFile),
	}
}

// GenerateRunID returns a fresh run id: <YYYYMMDD>T<HHMMSS>Z_<6 hex>.
//
// Letters, digits and underscores only, because it becomes part of a Cumulocity
// fragment name - which is why an ISO timestamp cannot be used verbatim.
func GenerateRunID(now time.Time) string {
	var suffix [3]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return now.UTC().Format("20060102T150405Z")
	}
	return now.UTC().Format("20060102T150405Z") + "_" + hex.EncodeToString(suffix[:])
}

// ErrAborted means the confirmation was declined.
var ErrAborted = errors.New("aborted")

// Run performs the migration.
func (m *Migrator) Run(ctx context.Context) (err error) {
	// Resume is validated first: it reads the journal, touches no network, and a
	// wrong run id should fail before a tenant is contacted or a staging
	// directory exists.
	incremental := m.opts.Incremental
	if m.opts.Resume != "" {
		incremental = true
		if err := m.resolveResume(); err != nil {
			return err
		}
	} else {
		m.runID = GenerateRunID(time.Now())
	}

	if !m.opts.NoFlag {
		m.markerRunID = m.runID
	} else if incremental {
		// Unmarked, but the id still has to be reportable or the run could
		// never be resumed.
		fmt.Fprintf(m.stderr, "Run id %s, created objects are not marked\n", m.runID)
	}

	if !m.opts.Force {
		if !m.confirm() {
			return ErrAborted
		}
	}

	m.stagingDirectory, err = os.MkdirTemp("", "c8y-devicemigration-staging.")
	if err != nil {
		return err
	}
	// Installed here, after the checks above, so a refusal prints no staging or
	// revert hints for something it never touched.
	defer func() { m.onExit(err) }()

	if incremental && m.opts.Resume == "" {
		if err := m.journal.Start(m.runID); err != nil {
			return err
		}
	}

	if incremental {
		return m.runIncremental(ctx)
	}
	return m.runChained(ctx)
}

// runChained writes the whole export to disk and then imports all of it.
//
// Simple, and fine until the dataset outgrows the staging directory.
func (m *Migrator) runChained(ctx context.Context) error {
	directory := filepath.Join(m.stagingDirectory, "export")

	fmt.Fprintln(m.stderr, "Exporting from the source tenant")
	if err := m.export(ctx, m.opts.RootID, directory, exportShape{
		childTypes: m.opts.ChildTypes,
		dataTypes:  m.opts.DataTypes,
	}); err != nil {
		return err
	}

	if err := m.register(ctx, directory); err != nil {
		return err
	}

	fmt.Fprintln(m.stderr, "Importing into the target tenant")
	m.importStarted = true
	return m.importFrom(ctx, directory, importer.Options{AdoptRegistered: m.opts.Credentials})
}

// runIncremental moves the hierarchy first and then the data one device at a
// time.
//
// Metadata is small however large the migration is; the data is the part that is
// not bounded. So the disk this needs is one device's data, not the whole tree's.
func (m *Migrator) runIncremental(ctx context.Context) error {
	metadata := filepath.Join(m.stagingDirectory, "metadata")

	fmt.Fprintln(m.stderr, "Exporting the hierarchy from the source tenant")
	// The data types are passed even though no data is written, so the limits
	// are still checked against what was asked for - an oversized migration has
	// to be refused before anything is created.
	if err := m.export(ctx, m.opts.RootID, metadata, exportShape{
		childTypes:   m.opts.ChildTypes,
		dataTypes:    m.opts.DataTypes,
		metadataOnly: true,
	}); err != nil {
		return err
	}

	if err := m.register(ctx, metadata); err != nil {
		return err
	}

	if m.opts.Resume != "" {
		// Re-imported rather than skipped: it is idempotent, and it is what
		// repairs a hierarchy that stopped half way or that an object was
		// deleted from.
		fmt.Fprintln(m.stderr, "Reconciling the hierarchy in the target tenant")
	} else {
		fmt.Fprintln(m.stderr, "Importing the hierarchy into the target tenant")
	}
	m.importStarted = true
	if err := m.importFrom(ctx, metadata, importer.Options{
		Resume:          m.opts.Resume != "",
		AdoptRegistered: m.opts.Credentials,
	}); err != nil {
		return err
	}

	if len(m.opts.DataTypes) == 0 {
		return nil
	}

	// The exported object files are the set of devices this run touched, which
	// is more precise than the mapping file if that was reused.
	files, err := importer.ObjectFiles(metadata)
	if err != nil {
		return err
	}

	fmt.Fprintf(m.stderr, "Migrating data for %d %s, one at a time\n",
		len(files), plural(len(files), "device", "devices"))
	for _, id := range m.journal.Interrupted() {
		fmt.Fprintf(m.stderr,
			"Run stopped while migrating data for (%s), so its records are delivered again and that device can end up with duplicates\n",
			id)
	}

	data := filepath.Join(m.stagingDirectory, "data")

	for index, file := range files {
		oldID := importer.SourceID(file)

		// The index counts every device, skipped or not, so the numbering stays
		// stable across resumes.
		if m.journal.Has("data " + oldID) {
			fmt.Fprintf(m.stderr, "Device %d of %d: data already migrated for (%s)\n",
				index+1, len(files), oldID)
			continue
		}
		fmt.Fprintf(m.stderr, "Device %d of %d\n", index+1, len(files))

		// Noted before the work and cleared by the "data" line after it, so a
		// journal holding "started X" with no "data X" identifies exactly the
		// device that was in flight when a run stopped.
		if err := m.journal.Note("started " + oldID); err != nil {
			return err
		}

		_ = os.RemoveAll(data)
		if err := m.export(ctx, oldID, data, exportShape{
			dataTypes: m.opts.DataTypes,
			dataOnly:  true,
		}); err != nil {
			return err
		}
		if err := m.importFrom(ctx, data, importer.Options{DataOnly: true}); err != nil {
			return err
		}

		// Journalled only once the data is in the target, so an interruption
		// part way through a device means that device is done again rather than
		// skipped. Repeating one device's data is recoverable; losing it
		// silently is not.
		if err := m.journal.Note("data " + oldID); err != nil {
			return err
		}
		// Discarded straight away: holding one device's data is the whole point.
		_ = os.RemoveAll(data)
	}
	return nil
}

// exportShape is the part of the export options that varies per phase.
type exportShape struct {
	childTypes   []export.ChildType
	dataTypes    []export.DataType
	metadataOnly bool
	dataOnly     bool
}

func (m *Migrator) export(ctx context.Context, rootID, directory string, shape exportShape) error {
	return export.New(m.source, m.stderr, export.Options{
		RootID:          rootID,
		OutputDirectory: directory,
		ChildTypes:      shape.childTypes,
		DataTypes:       shape.dataTypes,
		Windows:         m.opts.Windows,
		IgnoreLimits:    m.opts.IgnoreLimits,
		MetadataOnly:    shape.metadataOnly,
		DataOnly:        shape.dataOnly,
	}).Run(ctx)
}

// register provisions device users for the exported devices, and writes the
// credentials file.
//
// Between the export and the import, which is the whole of Option A: bulk
// registration creates each managed object owned by the device user it creates
// alongside it, and the import then fills that object in rather than creating a
// second one.
//
// The file is written the moment registration returns, before the import is
// attempted. By then the device users exist in the target, and the passwords are
// only in this process's memory - so an import failure must not be able to take
// them with it. A device whose password is lost is unreachable until somebody
// rotates it.
func (m *Migrator) register(ctx context.Context, directory string) error {
	if !m.opts.Credentials {
		return nil
	}

	// A resume never re-registers. Registration for an id the target already
	// knows rotates its password, which would invalidate the credentials file
	// the first attempt handed over - and the devices in it are the ones that
	// were already working.
	if m.opts.Resume != "" && m.journal.Has("registering") {
		fmt.Fprintln(m.stderr,
			"This run already registered its devices, so no credentials are issued again:")
		fmt.Fprintln(m.stderr,
			"  re-registering would rotate the passwords and invalidate the file from the first attempt.")
		fmt.Fprintln(m.stderr,
			"  To issue fresh credentials deliberately, use `c8y-devicemigration register` on its own.")
		return nil
	}

	// Journalled before the call, not after: if the run dies between the POST
	// and the note, a resume that re-registered would rotate passwords the
	// operator may already hold.
	if err := m.journal.Note("registering"); err != nil {
		return err
	}

	fmt.Fprintln(m.stderr, "Registering the devices in the target tenant")
	registrar := registration.New(m.target, m.stderr, registration.Options{
		InputDirectory: directory,
		Marker:         markerFragment(m.markerRunID),
		IdentityType:   m.opts.IdentityType,
		Tenant:         m.targetSession.Tenant,
	})

	credentials, runErr := registrar.Run(ctx)
	// Kept even when runErr is non-nil: a partial registration still created
	// device users, and the credentials for those are in this response and
	// nowhere else.
	m.credentials = credentials
	m.registeredCount = len(credentials)
	if len(credentials) > 0 && m.opts.CredentialsFile != "" {
		if err := m.writeCredentials(credentials); err != nil {
			return err
		}
	}
	if runErr != nil {
		return runErr
	}
	return nil
}

// writeCredentials writes the file and reports where it is.
//
// 0600, and never to stdout: the file is a list of working device passwords, and
// stdout lands in shell history, CI logs and anything the caller pipes into.
func (m *Migrator) writeCredentials(credentials []registration.Credential) error {
	file, err := os.OpenFile(m.opts.CredentialsFile, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("cannot write the credentials file: %w", err)
	}
	if err := registration.WriteCredentialsFile(file, credentials); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("cannot write the credentials file: %w", err)
	}

	m.credentialsWritten = true
	fmt.Fprintf(m.stderr, "Credentials for %d %s written to %s\n",
		len(credentials), plural(len(credentials), "device", "devices"), m.opts.CredentialsFile)
	fmt.Fprintln(m.stderr,
		"This is the only copy: the passwords are not recorded anywhere else, and re-issuing them rotates them.")
	return nil
}

// markerFragment is the fragment name for a run id, or empty when unmarked.
func markerFragment(runID string) string {
	if runID == "" {
		return ""
	}
	return importer.MarkerPrefix + runID
}

func (m *Migrator) importFrom(ctx context.Context, directory string, shape importer.Options) error {
	shape.InputDirectory = directory
	shape.MappingFile = m.opts.MappingFile
	shape.RunID = m.markerRunID
	shape.NoFlag = m.opts.NoFlag
	// The two tenants were already established as different by the pre-flight,
	// so the per-import check has nothing left to find.
	shape.AllowSameTenant = true

	return importer.New(m.target, m.stderr, shape).Run(ctx)
}

// confirm asks once, before anything is created, in the shape c8y asks.
func (m *Migrator) confirm() bool {
	action := fmt.Sprintf("Migrate %s from %s (%s) to %s (%s)",
		m.opts.RootID,
		session.NormalisedHost(m.sourceSession.Host), m.sourceSession.Tenant,
		session.NormalisedHost(m.targetSession.Host), m.targetSession.Tenant)

	if m.opts.Credentials {
		// Said before the confirmation, because it is the part that creates
		// something outside the inventory: a user per device, in the target's
		// user store, that the operator then has to distribute.
		action += ", provisioning a device user for each device"
	}

	if confirm.Ask(m.stderr, m.stdin, action) == confirm.Yes {
		return true
	}
	fmt.Fprintln(m.stderr, "Aborted, nothing was migrated")
	return false
}

// resolveResume reads the run this journal belongs to, rather than trusting the
// caller: a mismatched run id fails here instead of importing a second copy of
// everything.
func (m *Migrator) resolveResume() error {
	wanted := m.opts.Resume

	if err := importer.ValidateRunID(wanted); err != nil {
		return fmt.Errorf("Invalid --resume '%s': a run id is letters, digits and underscores only", wanted)
	}

	if !m.journal.Exists() {
		return fmt.Errorf(
			"There is no journal at %s, so there is nothing to resume.\n"+
				"Only an --incremental migration writes one. A chained migration imports all\n"+
				"the data in one step, so resuming it could deliver the same records twice.",
			m.journal.Path())
	}
	if !m.journal.Has("runId " + wanted) {
		return fmt.Errorf(
			"The journal at %s does not belong to run %s.\n"+
				"It records: runId %s",
			m.journal.Path(), wanted, m.journal.RunID())
	}

	m.runID = wanted
	return nil
}

// onExit cleans up, and on a failure says how to undo what was created.
func (m *Migrator) onExit(runErr error) {
	if runErr == nil {
		m.cleanupStaging()
		fmt.Fprintln(m.stderr, "Migration complete. Reverse it with:")
		m.printRevertHint()
		return
	}

	if m.credentialsWritten {
		fmt.Fprintf(m.stderr,
			"%d %s were registered before this failed, and their credentials are in %s.\n",
			m.registeredCount, plural(m.registeredCount, "device", "devices"), m.opts.CredentialsFile)
		fmt.Fprintln(m.stderr,
			"Those device users exist in the target tenant whether or not the migration finished;")
		fmt.Fprintln(m.stderr,
			"a revert removes them along with the objects they own.")
	}

	if m.stagingDirectory != "" {
		if _, err := os.Stat(m.stagingDirectory); err == nil {
			fmt.Fprintf(m.stderr,
				"Staging directory kept at %s, so a retry does not have to read the source tenant again\n",
				m.stagingDirectory)
		}
	}

	// An empty or absent mapping means nothing was ever created, so there is
	// nothing to revert and saying otherwise would only mislead.
	if !m.mappingHasEntries() {
		return
	}
	if m.importStarted {
		fmt.Fprintln(m.stderr, "The migration stopped part way through. Undo what it created with:")
	} else {
		fmt.Fprintln(m.stderr, "This attempt created nothing. What an earlier attempt left in the target")
		fmt.Fprintln(m.stderr, "tenant is still there, and can be removed with:")
	}
	m.printRevertHint()
}

func (m *Migrator) cleanupStaging() {
	if m.stagingDirectory == "" {
		return
	}
	if m.opts.KeepStaging {
		fmt.Fprintf(m.stderr, "Staging directory kept at %s\n", m.stagingDirectory)
		return
	}
	_ = os.RemoveAll(m.stagingDirectory)
}

func (m *Migrator) printRevertHint() {
	fmt.Fprintf(m.stderr, "  c8y-devicemigration revert --mapping %s --session %s\n",
		m.opts.MappingFile, m.targetSession.Path)
	if m.markerRunID != "" {
		fmt.Fprintln(m.stderr, "  or, if the mapping is lost:")
		fmt.Fprintf(m.stderr, "  c8y-devicemigration revert --runId %s --session %s\n",
			m.markerRunID, m.targetSession.Path)
	}
}

func (m *Migrator) mappingHasEntries() bool {
	body, err := os.ReadFile(m.opts.MappingFile)
	if err != nil {
		return false
	}
	return strings.Contains(string(body), ":")
}

func plural(n int, singular, many string) string {
	if n == 1 {
		return singular
	}
	return many
}
