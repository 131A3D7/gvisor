// Copyright 2018 The gVisor Authors.
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

package specutils

import (
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"

	specs "github.com/opencontainers/runtime-spec/specs-go"
	"gvisor.dev/gvisor/pkg/sentry/devices/nvproxy/nvconf"
	"gvisor.dev/gvisor/runsc/config"
)

func TestWaitForReadyHappy(t *testing.T) {
	cmd := exec.Command("/bin/sleep", "1000")
	if err := cmd.Start(); err != nil {
		t.Fatalf("cmd.Start() failed, err: %v", err)
	}
	defer func() { _ = cmd.Wait() }()

	var count int
	err := WaitForReady(cmd.Process.Pid, 5*time.Second, func() (bool, error) {
		if count < 3 {
			count++
			return false, nil
		}
		return true, nil
	})
	if err != nil {
		t.Errorf("ProcessWaitReady got: %v, expected: nil", err)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Errorf("cmd.ProcessKill(): %v", err)
	}
}

func TestWaitForReadyFail(t *testing.T) {
	cmd := exec.Command("/bin/sleep", "1000")
	if err := cmd.Start(); err != nil {
		t.Fatalf("cmd.Start() failed, err: %v", err)
	}
	defer func() { _ = cmd.Wait() }()

	var count int
	err := WaitForReady(cmd.Process.Pid, 5*time.Second, func() (bool, error) {
		if count < 3 {
			count++
			return false, nil
		}
		return false, fmt.Errorf("fake error")
	})
	if err == nil {
		t.Errorf("ProcessWaitReady got: nil, expected: error")
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Errorf("cmd.ProcessKill(): %v", err)
	}
}

func TestWaitForReadyNotRunning(t *testing.T) {
	cmd := exec.Command("/bin/true")
	if err := cmd.Start(); err != nil {
		t.Fatalf("cmd.Start() failed, err: %v", err)
	}
	defer func() { _ = cmd.Wait() }()

	err := WaitForReady(cmd.Process.Pid, 5*time.Second, func() (bool, error) {
		return false, nil
	})
	if err != nil && !strings.Contains(err.Error(), "terminated") {
		t.Errorf("ProcessWaitReady got: %v, expected: process terminated", err)
	}
	if err == nil {
		t.Errorf("ProcessWaitReady incorrectly succeeded")
	}
}

func TestWaitForReadyTimeout(t *testing.T) {
	cmd := exec.Command("/bin/sleep", "1000")
	if err := cmd.Start(); err != nil {
		t.Fatalf("cmd.Start() failed, err: %v", err)
	}
	defer func() { _ = cmd.Wait() }()

	err := WaitForReady(cmd.Process.Pid, 50*time.Millisecond, func() (bool, error) {
		return false, nil
	})
	if err == nil || !strings.Contains(err.Error(), "not running yet") {
		t.Errorf("ProcessWaitReady got: %v, expected: not running yet", err)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Errorf("cmd.ProcessKill(): %v", err)
	}
}

func TestSpecInvalid(t *testing.T) {
	for _, test := range []struct {
		name  string
		spec  specs.Spec
		error string
	}{
		{
			name: "valid",
			spec: specs.Spec{
				Root: &specs.Root{Path: "/"},
				Process: &specs.Process{
					Args: []string{"/bin/true"},
				},
				Mounts: []specs.Mount{
					{
						Source:      "src",
						Destination: "/dst",
					},
				},
			},
			error: "",
		},
		{
			name: "valid+warning",
			spec: specs.Spec{
				Root: &specs.Root{Path: "/"},
				Process: &specs.Process{
					Args: []string{"/bin/true"},
					// This is normally set by docker and will just cause warnings to be logged.
					ApparmorProfile: "someprofile",
				},
				// This is normally set by docker and will just cause warnings to be logged.
				Linux: &specs.Linux{Seccomp: &specs.LinuxSeccomp{}},
			},
			error: "",
		},
		{
			name: "no root",
			spec: specs.Spec{
				Process: &specs.Process{
					Args: []string{"/bin/true"},
				},
			},
			error: "must be defined",
		},
		{
			name: "empty root",
			spec: specs.Spec{
				Root: &specs.Root{},
				Process: &specs.Process{
					Args: []string{"/bin/true"},
				},
			},
			error: "must be defined",
		},
		{
			name: "no process",
			spec: specs.Spec{
				Root: &specs.Root{Path: "/"},
			},
			error: "must be defined",
		},
		{
			name: "empty args",
			spec: specs.Spec{
				Root:    &specs.Root{Path: "/"},
				Process: &specs.Process{},
			},
			error: "must be defined",
		},
		{
			name: "selinux",
			spec: specs.Spec{
				Root: &specs.Root{Path: "/"},
				Process: &specs.Process{
					Args:         []string{"/bin/true"},
					SelinuxLabel: "somelabel",
				},
			},
			error: "is not supported",
		},
		{
			name: "solaris",
			spec: specs.Spec{
				Root: &specs.Root{Path: "/"},
				Process: &specs.Process{
					Args: []string{"/bin/true"},
				},
				Solaris: &specs.Solaris{},
			},
			error: "is not supported",
		},
		{
			name: "windows",
			spec: specs.Spec{
				Root: &specs.Root{Path: "/"},
				Process: &specs.Process{
					Args: []string{"/bin/true"},
				},
				Windows: &specs.Windows{},
			},
			error: "is not supported",
		},
		{
			name: "relative mount destination",
			spec: specs.Spec{
				Root: &specs.Root{Path: "/"},
				Process: &specs.Process{
					Args: []string{"/bin/true"},
				},
				Mounts: []specs.Mount{
					{
						Source:      "src",
						Destination: "dst",
					},
				},
			},
			error: "must be an absolute path",
		},
		{
			name: "invalid mount option",
			spec: specs.Spec{
				Root: &specs.Root{Path: "/"},
				Process: &specs.Process{
					Args: []string{"/bin/true"},
				},
				Mounts: []specs.Mount{
					{
						Source:      "/src",
						Destination: "/dst",
						Type:        "bind",
						Options:     []string{"shared"},
					},
				},
			},
			error: "is not supported",
		},
		{
			name: "invalid rootfs propagation",
			spec: specs.Spec{
				Root: &specs.Root{Path: "/"},
				Process: &specs.Process{
					Args: []string{"/bin/true"},
				},
				Linux: &specs.Linux{
					RootfsPropagation: "foo",
				},
			},
			error: "root mount propagation option must specify private or slave",
		},
	} {
		err := ValidateSpec(&test.spec)
		if len(test.error) == 0 {
			if err != nil {
				t.Errorf("ValidateSpec(%q) failed, err: %v", test.name, err)
			}
		} else {
			if err == nil || !strings.Contains(err.Error(), test.error) {
				t.Errorf("ValidateSpec(%q) wrong error, got: %v, want: .*%s.*", test.name, err, test.error)
			}
		}
	}
}

func TestSeccomp(t *testing.T) {
	const containerName = "cont1"
	for _, tc := range []struct {
		name           string
		spec           specs.Spec
		seccompPresent bool
	}{
		{
			name:           "seccomp set",
			seccompPresent: true,
			spec: specs.Spec{
				Annotations: map[string]string{
					annotationContainerName: containerName,
				},
				Linux: &specs.Linux{
					Seccomp: &specs.LinuxSeccomp{},
				},
			},
		},
		{
			name:           "another container",
			seccompPresent: true,
			spec: specs.Spec{
				Annotations: map[string]string{
					annotationContainerName:     containerName,
					annotationSeccomp + "cont2": annotationSeccompRuntimeDefault,
				},
				Linux: &specs.Linux{
					Seccomp: &specs.LinuxSeccomp{},
				},
			},
		},
		{
			name:           "not RuntimeDefault",
			seccompPresent: true,
			spec: specs.Spec{
				Annotations: map[string]string{
					annotationContainerName:           containerName,
					annotationSeccomp + containerName: "foobar",
				},
				Linux: &specs.Linux{
					Seccomp: &specs.LinuxSeccomp{},
				},
			},
		},
		{
			name:           "not RuntimeDefault many names",
			seccompPresent: true,
			spec: specs.Spec{
				Annotations: map[string]string{
					annotationContainerName:           containerName,
					annotationSeccomp + containerName: "foobar",
					annotationSeccomp + "cont2":       annotationSeccompRuntimeDefault,
				},
				Linux: &specs.Linux{
					Seccomp: &specs.LinuxSeccomp{},
				},
			},
		},
		{
			name: "remove",
			spec: specs.Spec{
				Annotations: map[string]string{
					annotationContainerName:           containerName,
					annotationSeccomp + containerName: annotationSeccompRuntimeDefault,
				},
				Linux: &specs.Linux{
					Seccomp: &specs.LinuxSeccomp{},
				},
			},
		},
		{
			name: "remove many names",
			spec: specs.Spec{
				Annotations: map[string]string{
					annotationContainerName:           containerName,
					annotationSeccomp + containerName: annotationSeccompRuntimeDefault,
					annotationSeccomp + "cont2":       "foobar",
				},
				Linux: &specs.Linux{
					Seccomp: &specs.LinuxSeccomp{},
				},
			},
		},
		{
			name: "remove-nonexistent",
			spec: specs.Spec{
				Annotations: map[string]string{
					annotationSeccomp + containerName: annotationSeccompRuntimeDefault,
				},
			},
		},
		{
			name: "empty",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.spec.Root = &specs.Root{}
			fixSpec(&tc.spec, "", nil)
			if tc.seccompPresent {
				if tc.spec.Linux == nil || tc.spec.Linux.Seccomp == nil {
					t.Errorf("seccomp is not in the spec: %+v", tc.spec)
				}
			} else if tc.spec.Linux != nil && tc.spec.Linux.Seccomp != nil {
				t.Errorf("seccomp is in the spec: %+v", tc.spec)
			}
		})
	}
}

func TestNvidiaDriverCapabilities(t *testing.T) {
	testAllowedCapsFlag := "utility,compute,graphics"
	testAllowedCaps, _, err := nvconf.DriverCapsFromString(testAllowedCapsFlag)
	if err != nil {
		t.Fatalf("nvconf.DriverCapsFromString(%q) failed: %v", testAllowedCapsFlag, err)
	}
	for _, tc := range []struct {
		name        string
		allowedCaps string
		noEnv       bool // If true, no env variable is set.
		envCaps     string
		legacy      bool
		want        nvconf.DriverCaps
	}{
		{
			name:        "unspecified",
			allowedCaps: testAllowedCapsFlag,
			noEnv:       true,
			want:        nvconf.DefaultDriverCaps,
		},
		{
			name:        "unspecified-legacy",
			allowedCaps: testAllowedCapsFlag,
			noEnv:       true,
			legacy:      true,
			want:        testAllowedCaps,
		},
		{
			name:        "empty",
			allowedCaps: testAllowedCapsFlag,
			envCaps:     "",
			want:        nvconf.DefaultDriverCaps,
		},
		{
			name:        "empty-legacy",
			allowedCaps: testAllowedCapsFlag,
			envCaps:     "",
			legacy:      true,
			want:        nvconf.DefaultDriverCaps,
		},
		{
			name:        "compute",
			allowedCaps: testAllowedCapsFlag,
			envCaps:     nvconf.CapCompute.String(),
			want:        nvconf.CapCompute,
		},
		{
			name:        "utility,graphics-legacy",
			allowedCaps: testAllowedCapsFlag,
			envCaps:     (nvconf.CapUtility | nvconf.CapGraphics).String(),
			legacy:      true,
			want:        nvconf.CapUtility | nvconf.CapGraphics,
		},
		{
			name:        "all",
			allowedCaps: testAllowedCapsFlag,
			envCaps:     nvconf.AllCapabilitiesName,
			want:        testAllowedCaps,
		},
		{
			name:        "all-all",
			allowedCaps: "all",
			envCaps:     nvconf.AllCapabilitiesName,
			want:        nvconf.SupportedDriverCaps,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var env []string
			if tc.legacy {
				env = append(env, fmt.Sprintf("%s=%s", cudaVersionEnv, "10.2.89"))
			}
			if tc.envCaps != "" || !tc.noEnv {
				env = append(env, fmt.Sprintf("%s=%s", nvidiaDriverCapsEnv, tc.envCaps))
			}
			got, err := NVProxyDriverCapsFromEnv(
				&specs.Spec{Process: &specs.Process{Env: env}},
				&config.Config{NVProxyAllowedDriverCapabilities: tc.allowedCaps},
			)
			if err != nil {
				t.Errorf("NVProxyDriverCapsFromEnv() failed, err: %v", err)
			}
			if got != tc.want {
				t.Errorf("NVProxyDriverCapsFromEnv() got: %v, want: %v", got, tc.want)
			}
		})
	}
}
