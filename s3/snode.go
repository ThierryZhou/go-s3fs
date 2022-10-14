package s3

import (
	"context"
	"io"
	"sync"
	"unicode/utf8"

	"github.com/ThierryZhou/go-s3fs/fs"
	"github.com/ThierryZhou/go-s3fs/fs/asyncreader"
)

type S3Node struct {
	// The mutex is to make sure Read() and Close() aren't called
	// concurrently.  Unfortunately the persistent connection loop
	// in http transport calls Read() after Do() returns on
	// CancelRequest so this race can happen when it apparently
	// shouldn't.
	mu      sync.Mutex // mutex protects these values
	in      io.Reader
	ctx     context.Context // current context for transfer - may change
	ci      *fs.ConfigInfo
	origIn  io.ReadCloser
	close   io.Closer
	size    int64
	name    string
	closed  bool          // set if the file is closed
	exit    chan struct{} // channel that will be closed when transfer is finished
	withBuf bool          // is using a buffered in
}

// WithBuffer - If the file is above a certain size it adds an Async reader
func (sno *S3Node) WithBuffer() *S3Node {
	// if already have a buffer then just return
	if sno.withBuf {
		return sno
	}
	sno.withBuf = true
	var buffers int
	if sno.size >= int64(sno.ci.BufferSize) || sno.size == -1 {
		buffers = int(int64(sno.ci.BufferSize) / asyncreader.BufferSize)
	} else {
		buffers = int(sno.size / asyncreader.BufferSize)
	}
	// On big files add a buffer
	if buffers > 0 {
		rc, err := asyncreader.New(sno.ctx, sno.origIn, buffers)
		if err != nil {
			fs.Errorf(sno.name, "Failed to make buffer: %v", err)
		} else {
			sno.in = rc
			sno.close = rc
		}
	}
	return sno
}

// HasBuffer - returns true if this Account has an AsyncReader with a buffer
func (sno *S3Node) HasBuffer() bool {
	sno.mu.Lock()
	defer sno.mu.Unlock()
	_, ok := sno.in.(*asyncreader.AsyncReader)
	return ok
}

// GetReader returns the underlying io.ReadCloser under any Buffer
func (sno *S3Node) GetReader() io.ReadCloser {
	sno.mu.Lock()
	defer sno.mu.Unlock()
	return sno.origIn
}

// GetAsyncReader returns the current AsyncReader or nil if Account is unbuffered
func (sno *S3Node) GetAsyncReader() *asyncreader.AsyncReader {
	sno.mu.Lock()
	defer sno.mu.Unlock()
	if asyncIn, ok := sno.in.(*asyncreader.AsyncReader); ok {
		return asyncIn
	}
	return nil
}

// StopBuffering stops the async buffer doing any more buffering
func (sno *S3Node) StopBuffering() {
	if asyncIn, ok := sno.in.(*asyncreader.AsyncReader); ok {
		asyncIn.StopBuffering()
	}
}

// Abandon stops the async buffer doing any more buffering
func (sno *S3Node) Abandon() {
	if asyncIn, ok := sno.in.(*asyncreader.AsyncReader); ok {
		asyncIn.Abandon()
	}
}

// UpdateReader updates the underlying io.ReadCloser stopping the
// async buffer (if any) and re-adding it
func (sno *S3Node) UpdateReader(ctx context.Context, in io.ReadCloser) {
	sno.mu.Lock()
	withBuf := sno.withBuf
	if withBuf {
		sno.Abandon()
		sno.withBuf = false
	}
	sno.in = in
	sno.ctx = ctx
	sno.close = in
	sno.origIn = in
	sno.closed = false
	if withBuf {
		sno.WithBuffer()
	}
	sno.mu.Unlock()
}

// Check the read before it has happened is valid returning the number
// of bytes remaining to read.
func (sno *S3Node) checkReadBefore() (bytesUntilLimit int64, err error) {
	// Check to see if context is cancelled
	if err = sno.ctx.Err(); err != nil {
		return 0, err
	}

	return bytesUntilLimit, nil
}

// Check the read call after the read has happened
func (sno *S3Node) checkReadAfter(bytesUntilLimit int64, n int, err error) (outN int, outErr error) {
	bytesUntilLimit -= int64(n)
	if bytesUntilLimit < 0 {
		// chop the overage off
		n += int(bytesUntilLimit)
		if n < 0 {
			n = 0
		}
	}
	return n, err
}

// read bytes from the io.Reader passed in and account them
func (sno *S3Node) read(in io.Reader, p []byte) (n int, err error) {
	bytesUntilLimit, err := sno.checkReadBefore()
	if err == nil {
		n, err = in.Read(p)
		n, err = sno.checkReadAfter(bytesUntilLimit, n, err)
	}
	return n, err
}

// Read bytes from the object - see io.Reader
func (sno *S3Node) Read(p []byte) (n int, err error) {
	sno.mu.Lock()
	defer sno.mu.Unlock()
	return sno.read(sno.in, p)
}

// Thin wrapper for w
type accountWriteTo struct {
	w   io.Writer
	sno *S3Node
}

// Write writes len(p) bytes from p to the underlying data stream. It
// returns the number of bytes written from p (0 <= n <= len(p)) and
// any error encountered that caused the write to stop early. Write
// must return a non-nil error if it returns n < len(p). Write must
// not modify the slice data, even temporarily.
//
// Implementations must not retain p.
func (awt *accountWriteTo) Write(p []byte) (n int, err error) {
	bytesUntilLimit, err := awt.sno.checkReadBefore()
	if err == nil {
		n, err = awt.w.Write(p)
		n, err = awt.sno.checkReadAfter(bytesUntilLimit, n, err)
	}
	return n, err
}

// WriteTo writes data to w until there's no more data to write or
// when an error occurs. The return value n is the number of bytes
// written. Any error encountered during the write is also returned.
func (sno *S3Node) WriteTo(w io.Writer) (n int64, err error) {
	sno.mu.Lock()
	in := sno.in
	sno.mu.Unlock()
	wrappedWriter := accountWriteTo{w: w, sno: sno}
	if do, ok := in.(io.WriterTo); ok {
		n, err = do.WriteTo(&wrappedWriter)
	} else {
		n, err = io.Copy(&wrappedWriter, in)
	}
	return
}

// AccountRead account having read n bytes
func (sno *S3Node) AccountRead(n int) (err error) {
	sno.mu.Lock()
	defer sno.mu.Unlock()
	bytesUntilLimit, err := sno.checkReadBefore()
	if err == nil {
		n, err = sno.checkReadAfter(bytesUntilLimit, n, err)
	}
	return err
}

// Close the object
func (sno *S3Node) Close() error {
	sno.mu.Lock()
	defer sno.mu.Unlock()
	if sno.closed {
		return nil
	}
	sno.closed = true
	if sno.close == nil {
		return nil
	}
	return sno.close.Close()
}

// Done with accounting - must be called to free accounting goroutine
func (sno *S3Node) Done() {
	sno.mu.Lock()
	defer sno.mu.Unlock()
	close(sno.exit)

}

// shortenName shortens in to size runes long
// If size <= 0 then in is left untouched
func shortenName(in string, size int) string {
	if size <= 0 {
		return in
	}
	if utf8.RuneCountInString(in) <= size {
		return in
	}
	name := []rune(in)
	size-- // don't count ellipsis rune
	suffixLength := size / 2
	prefixLength := size - suffixLength
	suffixStart := len(name) - suffixLength
	name = append(append(name[:prefixLength], '…'), name[suffixStart:]...)
	return string(name)
}

// OldStream returns the top io.Reader
func (sno *S3Node) OldStream() io.Reader {
	sno.mu.Lock()
	defer sno.mu.Unlock()
	return sno.in
}

// SetStream updates the top io.Reader
func (sno *S3Node) SetStream(in io.Reader) {
	sno.mu.Lock()
	sno.in = in
	sno.mu.Unlock()
}

// WrapStream wraps an io Reader so it will be accounted in the same
// way as account
func (sno *S3Node) WrapStream(in io.Reader) io.Reader {
	return &accountStream{
		sno: sno,
		in:  in,
	}
}

// accountStream accounts a single io.Reader into a parent *S3Node
type accountStream struct {
	sno *S3Node
	in  io.Reader
}

// OldStream return the underlying stream
func (a *accountStream) OldStream() io.Reader {
	return a.in
}

// SetStream set the underlying stream
func (a *accountStream) SetStream(in io.Reader) {
	a.in = in
}

// WrapStream wrap in in an accounter
func (a *accountStream) WrapStream(in io.Reader) io.Reader {
	return a.sno.WrapStream(in)
}

// Read bytes from the object - see io.Reader
func (a *accountStream) Read(p []byte) (n int, err error) {
	return a.sno.read(a.in, p)
}

// Accounter accounts a stream allowing the accounting to be removed and re-added
type Accounter interface {
	io.Reader
	OldStream() io.Reader
	SetStream(io.Reader)
	WrapStream(io.Reader) io.Reader
}

// WrapFn wraps an io.Reader (for accounting purposes usually)
type WrapFn func(io.Reader) io.Reader

// UnWrap unwraps a reader returning unwrapped and wrap, a function to
// wrap it back up again.  If `in` is an Accounter then this function
// will take the accounting unwrapped and wrap will put it back on
// again the new Reader passed in.
//
// This allows functions which wrap io.Readers to move the accounting
// to the end of the wrapped chain of readers.  This is very important
// if buffering is being introduced and if the Reader might be wrapped
// again.
func UnWrap(in io.Reader) (unwrapped io.Reader, wrap WrapFn) {
	sno, ok := in.(Accounter)
	if !ok {
		return in, func(r io.Reader) io.Reader { return r }
	}
	return sno.OldStream(), sno.WrapStream
}
