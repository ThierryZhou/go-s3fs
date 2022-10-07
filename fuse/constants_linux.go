// Copyright 2022 the Go-S3FUSE Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package fuse

import (
	"syscall"
)

const syscall_O_LARGEFILE = syscall.O_LARGEFILE
const syscall_O_NOATIME = syscall.O_NOATIME
