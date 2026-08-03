// Package proto holds the client/server service definition.  The Go
// code generated from it lives in proto/slgov1.
package proto

//go:generate protoc --proto_path=. --go_out=.. --go_opt=module=github.com/quark-idlemind/slgo --go-grpc_out=.. --go-grpc_opt=module=github.com/quark-idlemind/slgo slgo.proto
