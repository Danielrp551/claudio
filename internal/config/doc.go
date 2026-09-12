// Package config loads settings and holds what belongs to this machine alone.
//
// Local state is never synchronised with the workspace: promotion mode, the
// ghost cap, subscriptions, ghost assignments, and trust overrides. A workspace
// owner cannot make a member run more processes than that member allows.
//
// The ghost cap defaults to 8, a number chosen with the measured cost of a ghost
// in view rather than picked at random. See ADR-0003.
package config
