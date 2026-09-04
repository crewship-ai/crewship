// Package dockerfilesources guards the Dockerfile's backend stage against a
// root-level Go package it forgot to copy (#886 schemas/, #2328 config/).
//
// The stage copies source directories one by one instead of the whole
// checkout, so a new top-level package compiles everywhere except inside
// the image, where `go build` fails with "no required module provides
// package". Regular CI never notices because it builds from a full
// checkout; this test does, on every PR.
package dockerfilesources
