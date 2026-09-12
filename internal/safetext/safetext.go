// Package safetext makes text that somebody else wrote safe to place inside a
// message that a model will read.
//
// The problem here is not the one XML escaping solves. The reader on the other
// side is a language model reading a conversation, not a parser, so what matters
// is that nothing a sender writes can be read as the end of the block that holds
// it, or as the beginning of a message from somebody else. Two things follow.
//
// Sequences that name a delimiter the reader recognises are neutralised here,
// because the shape of a cross session message is fixed by Claude Code and
// cannot carry a secret. Blocks that this project delimits itself carry an
// identifier the sender cannot guess instead, which is stronger and is done in
// the trust package.
//
// The second half of the package is about the values that name people. A person
// name, a session name and a permission mode all arrive from somewhere else and
// all end up inside a sentence the receiver reads. A newline in one of them is
// enough to write a sentence of your own in the middle of that framing, so they
// are refused where they enter and cleaned again where they are rendered.
package safetext

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
)

// MaxFieldRunes is how long a value that names somebody or something may be.
//
// It is generous for a real name and far too short to hide a paragraph in.
const MaxFieldRunes = 96

// tagPattern matches anything that could be read as the start or the end of a
// cross session message.
//
// It is deliberately more tolerant than the real syntax, in two ways that both
// came from a fuzz target finding a way around a stricter version.
//
// Spaces are allowed inside, because a reader does not care about them and
// "< / cross-session-message >" says the same thing. Cleaning runs first and
// turns every other kind of space into an ordinary one, so plain \s is enough.
//
// The closing bracket is not required. Requiring it left "<cross-session-message
// from-name=\"your-user\"" through untouched, and a sender who writes the rest
// of the tag further down the message has still written a tag. Matching the
// name alone costs nothing: the only text it touches is text that names this
// protocol's own delimiter, and that text stays readable afterwards.
var tagPattern = regexp.MustCompile(`(?i)<\s*/?\s*cross-session-message`)

// Body returns text that is safe to place inside a message.
//
// It is idempotent. Text that has already been through it contains no sequence
// the pattern matches, so running it twice changes nothing, which is what lets
// the trust framing and the frame builder both apply it without coordinating.
func Body(s string) string {
	return tagPattern.ReplaceAllStringFunc(clean(s, true), func(m string) string {
		// Escaping only the opening bracket is enough to make the sequence
		// inert, and it keeps the rest readable. A reader still sees exactly
		// what the sender wrote, which matters because the people using this
		// tool quote its own protocol at each other.
		return "&lt;" + m[1:]
	})
}

// Field returns a value safe to render inside a sentence: one line, no control
// characters, no invisible characters, and short.
func Field(s string) string {
	s = strings.TrimSpace(collapseSpace(clean(s, false)))

	runes := []rune(s)
	if len(runes) > MaxFieldRunes {
		// The marker matters. A truncated name that looks whole is worse than
		// one that says it was cut.
		return string(runes[:MaxFieldRunes-3]) + "..."
	}
	return s
}

// CheckField reports why a value cannot be accepted, or nil when it can.
//
// It is the boundary check, and it refuses rather than repairs. Cleaning a name
// quietly at the door would mean the workspace stores one thing and the person
// who chose it believes another, and the first time that difference matters is
// the worst time to discover it.
func CheckField(kind, s string) error {
	if strings.TrimSpace(s) == "" {
		return fmt.Errorf("safetext: the %s is empty", kind)
	}
	if n := len([]rune(s)); n > MaxFieldRunes {
		return fmt.Errorf("safetext: the %s is %d characters, the limit is %d",
			kind, n, MaxFieldRunes)
	}
	if Field(s) != s {
		return fmt.Errorf(
			"safetext: the %s contains line breaks, control characters, or "+
				"invisible characters, which are not allowed because it is "+
				"rendered inside a sentence other people read", kind)
	}
	return nil
}

// clean removes what has no business in text somebody else wrote.
//
// Control characters other than a tab, and a newline where newlines are allowed,
// are dropped. So are the invisible and direction changing characters, which
// have no legitimate use in a message and several illegitimate ones: they can
// hide text, reorder what is displayed, and split a word the eye reads as whole.
// Every other kind of space becomes an ordinary space, so the patterns that run
// afterwards do not have to know about all of them.
func clean(s string, keepLines bool) string {
	// Line endings are normalised first, so the loop below only has to think
	// about one of them. A message written on Windows keeps its line breaks.
	s = lineEndings.Replace(s)

	var b strings.Builder
	b.Grow(len(s))

	for _, r := range s {
		switch {
		case r == '\n':
			if keepLines {
				b.WriteByte('\n')
			} else {
				b.WriteByte(' ')
			}
		case r == '\t':
			b.WriteByte('\t')
		case isInvisible(r):
			// Dropped on purpose, with nothing in their place.
		case unicode.IsControl(r):
			// Dropped on purpose.
		case unicode.IsSpace(r):
			b.WriteByte(' ')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

var lineEndings = strings.NewReplacer("\r\n", "\n", "\r", "\n")

// isInvisible reports the characters that are removed outright.
//
// They are the ones that occupy no space and change how the rest is read:
// zero width spaces and joiners, the bidirectional overrides, the invisible
// operators, the interlinear annotations, and the byte order mark.
// The code points are written as numbers rather than as literals on purpose.
// Several of them are invisible, and a source file that contains them is a file
// nobody can review by reading it.
func isInvisible(r rune) bool {
	switch {
	case r == 0x00ad: // soft hyphen
		return true
	case r >= 0x200b && r <= 0x200f: // zero width space, joiners, direction marks
		return true
	case r == 0x2028 || r == 0x2029: // line and paragraph separators
		return true
	case r >= 0x202a && r <= 0x202e: // bidirectional embedding and override
		return true
	case r >= 0x2060 && r <= 0x2064: // word joiner, invisible operators
		return true
	case r >= 0x2066 && r <= 0x2069: // bidirectional isolates
		return true
	case r >= 0xfff9 && r <= 0xfffb: // interlinear annotation
		return true
	case r == 0xfeff: // byte order mark
		return true
	}
	return false
}

var spaceRun = regexp.MustCompile(`[ \t]+`)

func collapseSpace(s string) string { return spaceRun.ReplaceAllString(s, " ") }
