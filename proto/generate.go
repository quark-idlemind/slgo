// Package proto holds the service definitions.
//
// slgo.proto is the link between a daemon holding grid connections and
// the clients attached to it; the Go code lives in proto/slgov1.
//
// script.proto is the seam between the programs that run LSL and the
// things that can run it -- a simulator, a viewer, a grid session --
// and its Go code lives in proto/scriptv1.  Nothing in this repository
// implements it yet; it is the contract a backend is written against,
// and a backend need not live here.
package proto

//go:generate protoc --proto_path=. --go_out=.. --go_opt=module=github.com/quark-idlemind/slgo --go-grpc_out=.. --go-grpc_opt=module=github.com/quark-idlemind/slgo slgo.proto
//go:generate protoc --proto_path=. --go_out=.. --go_opt=module=github.com/quark-idlemind/slgo --go-grpc_out=.. --go-grpc_opt=module=github.com/quark-idlemind/slgo script.proto
