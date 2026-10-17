package api

// Protocol versioning. A team's braid-server and each member's app are
// upgraded independently, so either side can be older. Both declare which
// API protocol they speak, and a mismatch is caught when the app connects,
// with a clear "update" message, rather than as some request failing later.
// The rules for changing these constants are in the root CLAUDE.md.

// Protocol is the client/server API version this build speaks. Bump it on
// any change to internal/api, the server's routes or event payloads that the
// other side could depend on.
//
//	1: versioning introduced
//	2: delivery tracking (Thread.Delivery, /v1/exports/planned, /v1/threads/{id}/delivery)
//	3: /v1/invites/redeem also accepts recovery codes, and ignores the name for them
const Protocol = 3

// MinClientProtocol is the oldest app this server still works with. Apps
// built before versioning existed count as protocol 0. Raise it (to the new
// Protocol) when the server stops supporting something older apps rely on.
// Still 0: apps from before delivery tracking ignore it and keep working, and
// they can redeem recovery codes too.
const MinClientProtocol = 0

// MinServerProtocol is the oldest server this app works with. Raise it (to
// the new Protocol) when the app starts relying on something older servers
// don't have. 2: the app reads and reports delivery.
const MinServerProtocol = 2

// ProtocolHeader carries the app's Protocol on every request to the server.
const ProtocolHeader = "Braid-Protocol"

// CodeClientOutdated is the error code the server returns, with status 426,
// to apps older than its MinClientProtocol.
const CodeClientOutdated = "client_outdated"
