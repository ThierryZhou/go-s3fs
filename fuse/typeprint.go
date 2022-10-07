// Copyright 2022 the Go-S3FUSE Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package fuse

func (a *Attr) String() string {
	return Print(a)
}
