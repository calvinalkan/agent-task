package ticket

import "errors"

// Status constants.
const (
	StatusOpen       = "open"
	StatusInProgress = "in_progress"
	StatusClosed     = "closed"
)

// Type constants.
const (
	TypeBug     = "bug"
	TypeFeature = "feature"
	TypeTask    = "task"
	TypeEpic    = "epic"
	TypeChore   = "chore"
)

// Frontmatter delimiter.
const frontmatterDelimiter = "---"

// Field name for closed timestamp.
const fieldClosed = "closed"

// Error variables for ticket operations.
var (
	ErrConfigFileNotFound         = errors.New("config file not found")
	ErrConfigFileRead             = errors.New("cannot read config file")
	ErrConfigInvalid              = errors.New("invalid config file")
	ErrTicketDirEmpty             = errors.New("ticket-dir cannot be empty")
	ErrFlagRequiresArg            = errors.New("flag requires an argument")
	ErrUnknownFlag                = errors.New("unknown flag")
	ErrIDGenerationFailed         = errors.New("no unique id after repeated attempts")
	ErrTicketFileExists           = errors.New("ticket file already exists")
	ErrTicketNotFound             = errors.New("ticket not found")
	ErrIDRequired                 = errors.New("ticket ID is required")
	ErrTicketNotOpen              = errors.New("ticket is not open")
	ErrTicketNotStarted           = errors.New("ticket must be started first")
	ErrTicketAlreadyClosed        = errors.New("ticket is already closed")
	ErrTicketNotInProgress        = errors.New("ticket is not in_progress")
	ErrTicketNotClosed            = errors.New("ticket is not closed")
	ErrTicketAlreadyOpen          = errors.New("ticket is already open")
	ErrStatusNotFound             = errors.New("status field not found")
	ErrFieldInsertFailed          = errors.New("could not find insertion point for field")
	ErrClosedWithoutTimestamp     = errors.New("closed ticket missing closed timestamp")
	ErrClosedTimestampOnNonClosed = errors.New("closed timestamp on non-closed ticket")
	ErrBlockerIDRequired          = errors.New("blocker ID is required")
	ErrNotBlockedBy               = errors.New("ticket is not blocked by")
	ErrAlreadyBlockedBy           = errors.New("ticket is already blocked by")
	ErrCannotBlockSelf            = errors.New("ticket cannot block itself")
	ErrBlockedByNotFound          = errors.New("blocked-by field not found")
	ErrNoEditorFound              = errors.New("no editor found (set config.editor, $EDITOR, or install vi/nano)")
	ErrEditorFailed               = errors.New("editor failed")
	ErrEditModeRequired           = errors.New("must specify --start, --apply, or --launch (-l)")
	ErrEditModesExclusive         = errors.New("--start, --apply, and --launch are mutually exclusive")
	ErrEditInProgress             = errors.New("edit already in progress")
	ErrEditStale                  = errors.New("stale edit found")
	ErrEditNotStarted             = errors.New("no edit in progress (run --start first)")
	ErrEditBodyEmpty              = errors.New("body cannot be empty")
	ErrEditBodyNoHeading          = errors.New("body must contain a heading (# ...)")
	ErrMissingSchemaVersion       = errors.New("missing required field: schema_version")
	ErrUnsupportedSchemaVersion   = errors.New("unsupported schema_version")
	ErrParentNotFound             = errors.New("parent ticket not found")
	ErrParentClosed               = errors.New("parent ticket is closed")
	ErrParentNotStarted           = errors.New("parent ticket must be started first")
	ErrHasOpenChildren            = errors.New("ticket has open children")
)
