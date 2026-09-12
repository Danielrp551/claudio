package ccpeer

import "errors"

var (
	// ErrUnverifiedPlatform is returned by a platform operation that this
	// project has not verified against a real Claude Code session. It is not a
	// bug report, it is an honest refusal to guess.
	//
	// The caller is expected to degrade, which for the daemon means routing
	// through the MCP transport instead of publishing native peers, and telling
	// the user why.
	ErrUnverifiedPlatform = errors.New("ccpeer: this platform is not verified")

	// ErrNotFound means no session record matched.
	ErrNotFound = errors.New("ccpeer: no such session")

	// ErrAmbiguous means several live sessions answer to the same name. The
	// caller must disambiguate rather than pick one.
	ErrAmbiguous = errors.New("ccpeer: several sessions answer to that name")

	// ErrUnsupportedProtocol means a record or a frame carries a version this
	// build does not speak. Degrade, never guess.
	ErrUnsupportedProtocol = errors.New("ccpeer: unsupported protocol version")

	// ErrNoKey means the target session has no readable key file, so a platform
	// that requires an authentication line cannot deliver to it.
	ErrNoKey = errors.New("ccpeer: no key file for that session")

	// ErrMessageTooLarge means the serialized frame passes the cap Claude Code
	// enforces. Refusing here matches what the sender would do anyway, and it
	// produces a better message.
	ErrMessageTooLarge = errors.New("ccpeer: message is larger than the cap")
)
