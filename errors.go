package main

import "errors"

// Status constants.
const (
	StatusOpen       = "open"
	StatusInProgress = "in_progress"
	StatusClosed     = "closed"
)

// Frontmatter delimiter.
const frontmatterDelimiter = "---"

// Field name for closed timestamp.
const fieldClosed = "closed"

// Help flag.
const helpFlag = "--help"

var (
	errConfigFileNotFound         = errors.New("config file not found")
	errConfigFileRead             = errors.New("cannot read config file")
	errConfigInvalid              = errors.New("invalid config file")
	errTicketDirEmpty             = errors.New("ticket_dir cannot be empty")
	errFlagRequiresArg            = errors.New("flag requires an argument")
	errUnknownFlag                = errors.New("unknown flag")
	errIDGenerationFailed         = errors.New("failed to generate unique ID")
	errTicketFileExists           = errors.New("ticket file already exists")
	errTicketNotFound             = errors.New("ticket not found")
	errIDRequired                 = errors.New("ticket ID is required")
	errTicketNotOpen              = errors.New("ticket is not open")
	errTicketNotStarted           = errors.New("ticket must be started first")
	errTicketAlreadyClosed        = errors.New("ticket is already closed")
	errTicketNotInProgress        = errors.New("ticket is not in_progress")
	errTicketNotClosed            = errors.New("ticket is not closed")
	errTicketAlreadyOpen          = errors.New("ticket is already open")
	errStatusNotFound             = errors.New("status field not found")
	errFieldInsertFailed          = errors.New("could not find insertion point for field")
	errClosedWithoutTimestamp     = errors.New("closed ticket missing closed timestamp")
	errClosedTimestampOnNonClosed = errors.New("closed timestamp on non-closed ticket")
	errBlockerIDRequired          = errors.New("blocker ID is required")
	errNotBlockedBy               = errors.New("ticket is not blocked by")
	errAlreadyBlockedBy           = errors.New("ticket is already blocked by")
	errCannotBlockSelf            = errors.New("ticket cannot block itself")
	errBlockedByNotFound          = errors.New("blocked-by field not found")
	errNoEditorFound              = errors.New("no editor found (set config.editor, $EDITOR, or install vi/nano)")
)
