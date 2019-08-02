// Copyright 2019 The gVisor Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Binary proctor-go is a utility that facilitates language testing for Go.

// There are two types of Go tests: "Go tool tests" and "Go tests on disk".
// "Go tool tests" are found and executed using `go tool dist test`.
// "Go tests on disk" are found in the /test directory and are
// executed using `go run run.go`.
package main

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"gvisor.dev/gvisor/test/runtimes/common"
)

var (
	dir       = os.Getenv("LANG_DIR")
	goBin     = filepath.Join(dir, "bin/go")
	testDir   = filepath.Join(dir, "test")
	testRegEx = regexp.MustCompile(`^.+\.go$`)

	// Directories with .dir contain helper files for tests.
	// Exclude benchmarks and stress tests.
	dirFilter = regexp.MustCompile(`^(bench|stress)\/.+$|^.+\.dir.+$`)
)

type goRunner struct {
}

func main() {
	if err := common.LaunchFunc(goRunner{}); err != nil {
		log.Fatalf("Failed to start: %v", err)
	}
}

func (g goRunner) ListTests() ([]string, error) {
	// Go tool dist test tests.
	args := []string{"tool", "dist", "test", "-list"}
	cmd := exec.Command(filepath.Join(dir, "bin/go"), args...)
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to list: %v", err)
	}
	var toolSlice []string
	for _, test := range strings.Split(string(out), "\n") {
		toolSlice = append(toolSlice, test)
	}

	// Go tests on disk.
	diskSlice, err := common.Search(testDir, testRegEx)
	if err != nil {
		return nil, err
	}
	// Remove items from /bench/, /stress/ and .dir files
	diskFiltered := diskSlice[:0]
	for _, file := range diskSlice {
		if !dirFilter.MatchString(file) {
			diskFiltered = append(diskFiltered, file)
		}
	}

	return append(toolSlice, diskFiltered...), nil
}

func (g goRunner) RunTest(test string) error {
	// Check if test exists on disk by searching for file of the same name.
	// This will determine whether or not it is a Go test on disk.
	if strings.HasSuffix(test, ".go") {
		// Test has suffix ".go" which indicates a disk test, run it as such.
		cmd := exec.Command(goBin, "run", "run.go", "-v", "--", test)
		cmd.Dir = testDir
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("failed to run test: %v", err)
		}
	} else {
		// No ".go" suffix, run as a tool test.
		cmd := exec.Command(goBin, "tool", "dist", "test", "-run", test)
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("failed to run test: %v", err)
		}
	}
	return nil
}
