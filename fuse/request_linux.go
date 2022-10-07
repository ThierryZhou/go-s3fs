//go:build linux

// Copyright 2022 the Go-S3FUSE Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package fuse

const outputHeaderSize = 160

const (
	_FUSE_KERNEL_VERSION   = 7
	_MINIMUM_MINOR_VERSION = 12
	_OUR_MINOR_VERSION     = 28
)
