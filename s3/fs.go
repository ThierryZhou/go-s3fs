// Copyright 2022 the go-s3fs Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package s3

import (
	"archive/zip"
	"context"
	"io/ioutil"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	"github.com/ThierryZhou/go-s3fs/fs"
	"github.com/ThierryZhou/go-s3fs/fuse"
)

type s3Root struct {
	fs.Inode

	s3cli s3Client
}

var _ = (fs.NodeOnAdder)((*s3Root)(nil))

func (sr *s3Root) OnAdd(ctx context.Context) {
	for _, f := range sr.cli {
		if f.FileInfo().IsDir() {
			continue
		}
		dir, base := filepath.Split(filepath.Clean(f.Name))

		p := &sr.Inode
		for _, component := range strings.Split(dir, "/") {
			if len(component) == 0 {
				continue
			}
			ch := p.GetChild(component)
			if ch == nil {
				ch = p.NewPersistentInode(ctx, &fs.Inode{},
					fs.StableAttr{Mode: fuse.S_IFDIR})
				p.AddChild(component, ch, true)
			}

			p = ch
		}
		ch := p.NewPersistentInode(ctx, &s3File{file: f}, fs.StableAttr{})
		p.AddChild(base, ch, true)
	}
}

// NewS3Tree creates a new file-system for the zip file named name.
func NewS3Tree(name string, args string) (fs.InodeEmbedder, error) {
	r, err := NewS3Client(args)
	if err != nil {
		return nil, err
	}

	return &s3Root{s3cli: r}, nil
}

// s3File is a file read from a zip archive.
type s3File struct {
	fs.Inode
	file *zip.File

	mu   sync.Mutex
	data []byte
}

var _ = (fs.NodeOpener)((*s3File)(nil))
var _ = (fs.NodeReader)((*s3File)(nil))
var _ = (fs.NodeWriter)((*s3File)(nil))

var _ = (fs.NodeGetattrer)((*s3File)(nil))

// Open lazily unpacks zip data
func (zf *s3File) Open(ctx context.Context, flags uint32) (fs.FileHandle, uint32, syscall.Errno) {
	zf.mu.Lock()
	defer zf.mu.Unlock()
	if zf.data == nil {
		rc, err := zf.file.Open()
		if err != nil {
			return nil, 0, syscall.EIO
		}
		content, err := ioutil.ReadAll(rc)
		if err != nil {
			return nil, 0, syscall.EIO
		}

		zf.data = content
	}

	// We don't return a filehandle since we don't really need
	// one.  The file content is immutable, so hint the kernel to
	// cache the data.
	return nil, fuse.FOPEN_KEEP_CACHE, 0
}

// Getattr sets the minimum, which is the size. A more full-featured
// FS would also set timestamps and permissions.
func (zf *s3File) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	out.Mode = uint32(zf.file.Mode()) & 07777
	out.Nlink = 1
	out.Mtime = uint64(zf.file.ModTime().Unix())
	out.Atime = out.Mtime
	out.Ctime = out.Mtime
	out.Size = zf.file.UncompressedSize64
	const bs = 512
	out.Blksize = bs
	out.Blocks = (out.Size + bs - 1) / bs
	return 0
}

// Read simply returns the data that was already unpacked in the Open call
func (zf *s3File) Read(ctx context.Context, f fs.FileHandle, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	end := int(off) + len(dest)
	if end > len(zf.data) {
		end = len(zf.data)
	}
	return fuse.ReadResultData(zf.data[off:end]), 0
}

// Write simply returns the data that was already unpacked in the Open call
func (zf *s3File) Write(ctx context.Context, f fs.FileHandle, data []byte, off int64) (written uint32, errno syscall.Errno) {
	end := int(off) + len(data)
	if end > len(zf.data) {
		end = len(zf.data)
	}
	return fuse.Write(zf.data[off:end]), 0
}

func NewArchiveFileSystem(name string) (root fs.InodeEmbedder, err error) {

	root, err = NewS3Tree(name)
	if err != nil {
		return nil, err
	}

	return root, nil
}
