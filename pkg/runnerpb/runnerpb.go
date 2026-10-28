// Package runnerpb holds the protobuf/gRPC definitions of the runner/backend
// split protocol.
package runnerpb

// ProtocolVersion is bumped on any incompatible protocol change. Both sides
// exchange it in Register/RegisterResult and the backend rejects mismatches.
const ProtocolVersion = 1
