package api

// Protocol versioning. A team's braid-server and each member's app are
// upgraded independently, so either side can be older. Both declare which
// API protocol they speak, and a mismatch is caught when the app connects,
// with a clear "update" message, rather than as some request failing later.
// The rules for changing these constants are in the root CLAUDE.md.

// Protocol is the client/server API version this build speaks. Bump it on
// any change to internal/api, the server's routes or event payloads that the
// other side could depend on.
const Protocol = 1

// MinClientProtocol is the oldest app this server still works with. Apps
// built before versioning existed count as protocol 0. Raise it (to the new
// Protocol) when the server stops supporting something older apps rely on.
const MinClientProtocol = 0

// MinServerProtocol is the oldest server this app works with. Raise it (to
// the new Protocol) when the app starts relying on something older servers
// don't have.
const MinServerProtocol = 0

// ProtocolHeader carries the app's Protocol on every request to the server.
const ProtocolHeader = "Braid-Protocol"

// CodeClientOutdated is the error code the server returns, with status 426,
// to apps older than its MinClientProtocol.
const CodeClientOutdated = "client_outdated"
