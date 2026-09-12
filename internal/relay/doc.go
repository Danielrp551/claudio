// Package relay is the server side of the default transport.
//
// It routes envelopes between connectors and queues them for members who are
// offline. It is untrusted by design: it knows who is connected and which
// envelope goes to whom, and it cannot read what is inside. See ADR-0005.
//
// It has to be easy to stand up, with one command or one container, or it will
// not be self hosted in practice.
package relay
