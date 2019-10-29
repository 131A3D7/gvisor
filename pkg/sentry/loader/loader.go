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

// Package loader loads an executable file into a MemoryManager.
package loader

import (
	"bytes"
	"fmt"
	"io"
	"path"
	"strings"

	"gvisor.dev/gvisor/pkg/abi"
	"gvisor.dev/gvisor/pkg/abi/linux"
	"gvisor.dev/gvisor/pkg/cpuid"
	"gvisor.dev/gvisor/pkg/rand"
	"gvisor.dev/gvisor/pkg/sentry/arch"
	"gvisor.dev/gvisor/pkg/sentry/context"
	"gvisor.dev/gvisor/pkg/sentry/fs"
	"gvisor.dev/gvisor/pkg/sentry/kernel/auth"
	"gvisor.dev/gvisor/pkg/sentry/mm"
	"gvisor.dev/gvisor/pkg/sentry/usermem"
	"gvisor.dev/gvisor/pkg/syserr"
	"gvisor.dev/gvisor/pkg/syserror"
)

// LoadArgs holds specifications for an executable file to be loaded.
type LoadArgs struct {
	// MemoryManager is the memory manager to load the executable into.
	MemoryManager *mm.MemoryManager

	// Mounts is the mount namespace in which to look up Filename.
	Mounts *fs.MountNamespace

	// Root is the root directory under which to look up Filename.
	Root *fs.Dirent

	// WorkingDirectory is the working directory under which to look up
	// Filename.
	WorkingDirectory *fs.Dirent

	// RemainingTraversals is the maximum number of symlinks to follow to
	// resolve Filename. This counter is passed by reference to keep it
	// updated throughout the call stack.
	RemainingTraversals *uint

	// ResolveFinal indicates whether the final link of Filename should be
	// resolved, if it is a symlink.
	ResolveFinal bool

	// Filename is the path for the executable.
	Filename string

	// File is an open fs.File object of the executable. If File is not
	// nil, then File will be loaded and Filename will be ignored.
	File *fs.File

	// CloseOnExec indicates that the executable (or one of its parent
	// directories) was opened with O_CLOEXEC. If the executable is an
	// interpreter script, then cause an ENOENT error to occur, since the
	// script would otherwise be inaccessible to the interpreter.
	CloseOnExec bool

	// Argv is the vector of arguments to pass to the executable.
	Argv []string

	// Envv is the vector of environment variables to pass to the
	// executable.
	Envv []string

	// Features specifies the CPU feature set for the executable.
	Features *cpuid.FeatureSet
}

// readFull behaves like io.ReadFull for an *fs.File.
func readFull(ctx context.Context, f *fs.File, dst usermem.IOSequence, offset int64) (int64, error) {
	var total int64
	for dst.NumBytes() > 0 {
		n, err := f.Preadv(ctx, dst, offset+total)
		total += n
		if err == io.EOF && total != 0 {
			return total, io.ErrUnexpectedEOF
		} else if err != nil {
			return total, err
		}
		dst = dst.DropFirst64(n)
	}
	return total, nil
}

// openPath opens args.Filename and checks that it is valid for loading.
//
// openPath returns an *fs.Dirent and *fs.File for args.Filename, which is not
// installed in the Task FDTable. The caller takes ownership of both.
//
// args.Filename must be a readable, executable, regular file.
func openPath(ctx context.Context, args LoadArgs) (*fs.Dirent, *fs.File, error) {
	if args.Filename == "" {
		ctx.Infof("cannot open empty name")
		return nil, nil, syserror.ENOENT
	}

	var d *fs.Dirent
	var err error
	if args.ResolveFinal {
		d, err = args.Mounts.FindInode(ctx, args.Root, args.WorkingDirectory, args.Filename, args.RemainingTraversals)
	} else {
		d, err = args.Mounts.FindLink(ctx, args.Root, args.WorkingDirectory, args.Filename, args.RemainingTraversals)
	}
	if err != nil {
		return nil, nil, err
	}
	// Defer a DecRef for the sake of failure cases.
	defer d.DecRef()

	if !args.ResolveFinal && fs.IsSymlink(d.Inode.StableAttr) {
		return nil, nil, syserror.ELOOP
	}

	if err := checkPermission(ctx, d); err != nil {
		return nil, nil, err
	}

	// If they claim it's a directory, then make sure.
	//
	// N.B. we reject directories below, but we must first reject
	// non-directories passed as directories.
	if strings.HasSuffix(args.Filename, "/") && !fs.IsDir(d.Inode.StableAttr) {
		return nil, nil, syserror.ENOTDIR
	}

	if err := checkIsRegularFile(ctx, d, args.Filename); err != nil {
		return nil, nil, err
	}

	f, err := d.Inode.GetFile(ctx, d, fs.FileFlags{Read: true})
	if err != nil {
		return nil, nil, err
	}
	// Defer a DecRef for the sake of failure cases.
	defer f.DecRef()

	if err := checkPread(ctx, f, args.Filename); err != nil {
		return nil, nil, err
	}

	d.IncRef()
	f.IncRef()
	return d, f, err
}

// checkFile performs checks on a file to be executed.
func checkFile(ctx context.Context, f *fs.File, filename string) error {
	if err := checkPermission(ctx, f.Dirent); err != nil {
		return err
	}

	if err := checkIsRegularFile(ctx, f.Dirent, filename); err != nil {
		return err
	}

	return checkPread(ctx, f, filename)
}

// checkPermission checks whether the file is readable and executable.
func checkPermission(ctx context.Context, d *fs.Dirent) error {
	perms := fs.PermMask{
		// TODO(gvisor.dev/issue/160): Linux requires only execute
		// permission, not read. However, our backing filesystems may
		// prevent us from reading the file without read permission.
		//
		// Additionally, a task with a non-readable executable has
		// additional constraints on access via ptrace and procfs.
		Read:    true,
		Execute: true,
	}
	return d.Inode.CheckPermission(ctx, perms)
}

// checkIsRegularFile prevents us from trying to execute a directory, pipe, etc.
func checkIsRegularFile(ctx context.Context, d *fs.Dirent, filename string) error {
	attr := d.Inode.StableAttr
	if !fs.IsRegular(attr) {
		ctx.Infof("%s is not regular: %v", filename, attr)
		return syserror.EACCES
	}
	return nil
}

// checkPread checks whether we can read the file at arbitrary offsets.
func checkPread(ctx context.Context, f *fs.File, filename string) error {
	if !f.Flags().Pread {
		ctx.Infof("%s cannot be read at an offset: %+v", filename, f.Flags())
		return syserror.EACCES
	}
	return nil
}

// allocStack allocates and maps a stack in to any available part of the address space.
func allocStack(ctx context.Context, m *mm.MemoryManager, a arch.Context) (*arch.Stack, error) {
	ar, err := m.MapStack(ctx)
	if err != nil {
		return nil, err
	}
	return &arch.Stack{a, m, ar.End}, nil
}

const (
	// maxLoaderAttempts is the maximum number of attempts to try to load
	// an interpreter scripts, to prevent loops. 6 (initial + 5 changes) is
	// what the Linux kernel allows (fs/exec.c:search_binary_handler).
	maxLoaderAttempts = 6
)

// loadExecutable loads an executable that is pointed to by args.File. If nil,
// the path args.Filename is resolved and loaded. If the executable is an
// interpreter script rather than an ELF, the binary of the corresponding
// interpreter will be loaded.
//
// It returns:
//  * loadedELF, description of the loaded binary
//  * arch.Context matching the binary arch
//  * fs.Dirent of the binary file
//  * Possibly updated args.Argv
func loadExecutable(ctx context.Context, args LoadArgs) (loadedELF, arch.Context, *fs.Dirent, []string, error) {
	for i := 0; i < maxLoaderAttempts; i++ {
		var (
			d   *fs.Dirent
			err error
		)
		if args.File == nil {
			d, args.File, err = openPath(ctx, args)
			// We will return d in the successful case, but defer a DecRef for the
			// sake of intermediate loops and failure cases.
			if d != nil {
				defer d.DecRef()
			}
			if args.File != nil {
				defer args.File.DecRef()
			}
		} else {
			d = args.File.Dirent
			d.IncRef()
			defer d.DecRef()
			err = checkFile(ctx, args.File, args.Filename)
		}
		if err != nil {
			ctx.Infof("Error opening %s: %v", args.Filename, err)
			return loadedELF{}, nil, nil, nil, err
		}

		// Check the header. Is this an ELF or interpreter script?
		var hdr [4]uint8
		// N.B. We assume that reading from a regular file cannot block.
		_, err = readFull(ctx, args.File, usermem.BytesIOSequence(hdr[:]), 0)
		// Allow unexpected EOF, as a valid executable could be only three bytes
		// (e.g., #!a).
		if err != nil && err != io.ErrUnexpectedEOF {
			if err == io.EOF {
				err = syserror.ENOEXEC
			}
			return loadedELF{}, nil, nil, nil, err
		}

		switch {
		case bytes.Equal(hdr[:], []byte(elfMagic)):
			loaded, ac, err := loadELF(ctx, args)
			if err != nil {
				ctx.Infof("Error loading ELF: %v", err)
				return loadedELF{}, nil, nil, nil, err
			}
			// An ELF is always terminal. Hold on to d.
			d.IncRef()
			return loaded, ac, d, args.Argv, err
		case bytes.Equal(hdr[:2], []byte(interpreterScriptMagic)):
			if args.CloseOnExec {
				return loadedELF{}, nil, nil, nil, syserror.ENOENT
			}
			args.Filename, args.Argv, err = parseInterpreterScript(ctx, args.Filename, args.File, args.Argv)
			if err != nil {
				ctx.Infof("Error loading interpreter script: %v", err)
				return loadedELF{}, nil, nil, nil, err
			}
		default:
			ctx.Infof("Unknown magic: %v", hdr)
			return loadedELF{}, nil, nil, nil, syserror.ENOEXEC
		}
		// Set to nil in case we loop on a Interpreter Script.
		args.File = nil
	}

	return loadedELF{}, nil, nil, nil, syserror.ELOOP
}

// Load loads args.File into a MemoryManager. If args.File is nil, the path
// args.Filename is resolved and loaded instead.
//
// If Load returns ErrSwitchFile it should be called again with the returned
// path and argv.
//
// Preconditions:
//  * The Task MemoryManager is empty.
//  * Load is called on the Task goroutine.
func Load(ctx context.Context, args LoadArgs, extraAuxv []arch.AuxEntry, vdso *VDSO) (abi.OS, arch.Context, string, *syserr.Error) {
	// Load the executable itself.
	loaded, ac, d, newArgv, err := loadExecutable(ctx, args)
	if err != nil {
		return 0, nil, "", syserr.NewDynamic(fmt.Sprintf("Failed to load %s: %v", args.Filename, err), syserr.FromError(err).ToLinux())
	}
	defer d.DecRef()

	// Load the VDSO.
	vdsoAddr, err := loadVDSO(ctx, args.MemoryManager, vdso, loaded)
	if err != nil {
		return 0, nil, "", syserr.NewDynamic(fmt.Sprintf("Error loading VDSO: %v", err), syserr.FromError(err).ToLinux())
	}

	// Setup the heap. brk starts at the next page after the end of the
	// executable. Userspace can assume that the remainer of the page after
	// loaded.end is available for its use.
	e, ok := loaded.end.RoundUp()
	if !ok {
		return 0, nil, "", syserr.NewDynamic(fmt.Sprintf("brk overflows: %#x", loaded.end), linux.ENOEXEC)
	}
	args.MemoryManager.BrkSetup(ctx, e)

	// Allocate our stack.
	stack, err := allocStack(ctx, args.MemoryManager, ac)
	if err != nil {
		return 0, nil, "", syserr.NewDynamic(fmt.Sprintf("Failed to allocate stack: %v", err), syserr.FromError(err).ToLinux())
	}

	// Push the original filename to the stack, for AT_EXECFN.
	execfn, err := stack.Push(args.Filename)
	if err != nil {
		return 0, nil, "", syserr.NewDynamic(fmt.Sprintf("Failed to push exec filename: %v", err), syserr.FromError(err).ToLinux())
	}

	// Push 16 random bytes on the stack which AT_RANDOM will point to.
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return 0, nil, "", syserr.NewDynamic(fmt.Sprintf("Failed to read random bytes: %v", err), syserr.FromError(err).ToLinux())
	}
	random, err := stack.Push(b)
	if err != nil {
		return 0, nil, "", syserr.NewDynamic(fmt.Sprintf("Failed to push random bytes: %v", err), syserr.FromError(err).ToLinux())
	}

	c := auth.CredentialsFromContext(ctx)

	// Add generic auxv entries.
	auxv := append(loaded.auxv, arch.Auxv{
		arch.AuxEntry{linux.AT_UID, usermem.Addr(c.RealKUID.In(c.UserNamespace).OrOverflow())},
		arch.AuxEntry{linux.AT_EUID, usermem.Addr(c.EffectiveKUID.In(c.UserNamespace).OrOverflow())},
		arch.AuxEntry{linux.AT_GID, usermem.Addr(c.RealKGID.In(c.UserNamespace).OrOverflow())},
		arch.AuxEntry{linux.AT_EGID, usermem.Addr(c.EffectiveKGID.In(c.UserNamespace).OrOverflow())},
		// The conditions that require AT_SECURE = 1 never arise. See
		// kernel.Task.updateCredsForExecLocked.
		arch.AuxEntry{linux.AT_SECURE, 0},
		arch.AuxEntry{linux.AT_CLKTCK, linux.CLOCKS_PER_SEC},
		arch.AuxEntry{linux.AT_EXECFN, execfn},
		arch.AuxEntry{linux.AT_RANDOM, random},
		arch.AuxEntry{linux.AT_PAGESZ, usermem.PageSize},
		arch.AuxEntry{linux.AT_SYSINFO_EHDR, vdsoAddr},
	}...)
	auxv = append(auxv, extraAuxv...)

	sl, err := stack.Load(newArgv, args.Envv, auxv)
	if err != nil {
		return 0, nil, "", syserr.NewDynamic(fmt.Sprintf("Failed to load stack: %v", err), syserr.FromError(err).ToLinux())
	}

	m := args.MemoryManager
	m.SetArgvStart(sl.ArgvStart)
	m.SetArgvEnd(sl.ArgvEnd)
	m.SetEnvvStart(sl.EnvvStart)
	m.SetEnvvEnd(sl.EnvvEnd)
	m.SetAuxv(auxv)
	m.SetExecutable(d)

	ac.SetIP(uintptr(loaded.entry))
	ac.SetStack(uintptr(stack.Bottom))

	name := path.Base(args.Filename)
	if len(name) > linux.TASK_COMM_LEN-1 {
		name = name[:linux.TASK_COMM_LEN-1]
	}

	return loaded.os, ac, name, nil
}
