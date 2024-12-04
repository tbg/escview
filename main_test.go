// Copyright 2024 The Cockroach Authors.
//
// Use of this software is governed by the Business Source License
// included in the file licenses/BSL.txt.
//
// As of the Change Date specified in that file, in accordance with
// the Business Source License, use of this software will be governed
// by the Apache License, Version 2.0, included in the file
// licenses/APL.txt.

package main

import (
	"fmt"
	"testing"
)

func TestProcessLines(t *testing.T) {
	data := []byte(`
pkg/sql/crdb_internal.go:242:43: map[string]struct {}{...} escapes to heap:
pkg/sql/crdb_internal.go:242:43:   flow: {heap} = &{storage for map[string]struct {}{...}}:
pkg/sql/crdb_internal.go:242:43:     from map[string]struct {}{...} (spill) at pkg/sql/crdb_internal.go:242:43
pkg/sql/crdb_internal.go:242:43:     from SupportedVTables = map[string]struct {}{...} (assign) at pkg/sql/crdb_internal.go:242:5
pkg/sql/another_file.go:100:10: something else happens here:
pkg/sql/another_file.go:100:10:   another line
pkg/sql/crdb_internal.go:300:15: a different line in the same file
`)

	result, err := processLines(data)
	if err != nil {
		t.Fatal(err)
	}

	for k, v := range result {
		fmt.Printf("File: %s, Line: %d\n", k.file, k.line)
		fmt.Printf("Content:\n%s\n", v)
		fmt.Println("--------------------")
	}
}
