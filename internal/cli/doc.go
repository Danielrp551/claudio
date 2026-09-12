// Package cli defines the commands a person types.
//
// The design target is that configuration fits in one line, because a setting
// that needs a manual will not be used:
//
//	claudio trust ana@acme peer
//	claudio policy --max-ghosts 12
//	claudio promote luis/infra
//
// The status command carries a specific burden. Running several processes named
// claudio makes users suspicious, so status has to explain what each one is.
package cli
