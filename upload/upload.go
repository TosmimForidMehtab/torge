// Package upload handles multipart file uploads by streaming: parts are
// processed one at a time straight from the request body, so large files are
// never loaded into memory. Size and count limits are enforced while reading,
// content types are sniffed from the data, and filenames are sanitized.
//
//	fields, err := upload.Stream(c.Request(), upload.Options{
//	    MaxFileSize:  20 << 20,
//	    AllowedTypes: []string{"image/png", "image/jpeg"},
//	}, func(f *upload.File) error {
//	    _, err := storage.Put(c.Context(), newKey(), f, f.Meta())
//	    return err
//	})
//
// Register upload routes with torge.BodyLimit set to the largest acceptable
// request, since the application-wide body limit is usually small.
package upload

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/TosmimForidMehtab/torge"
)

// Error codes.
const (
	CodeNotMultipart   = "UPLOAD_NOT_MULTIPART"
	CodeFileTooLarge   = "UPLOAD_FILE_TOO_LARGE"
	CodeTooManyFiles   = "UPLOAD_TOO_MANY_FILES"
	CodeTypeNotAllowed = "UPLOAD_TYPE_NOT_ALLOWED"
	CodeFieldTooLarge  = "UPLOAD_FIELD_TOO_LARGE"
	CodeMalformed      = "UPLOAD_MALFORMED"
)

// Options limits an upload.
type Options struct {
	// MaxFileSize bounds each file (default 10 MiB).
	MaxFileSize int64
	// MaxFiles bounds the number of files (default 10).
	MaxFiles int
	// MaxFieldSize bounds each non-file field (default 64 KiB).
	MaxFieldSize int64
	// MaxFields bounds the number of non-file fields (default 100).
	MaxFields int
	// AllowedTypes restricts sniffed content types (for example
	// "image/png"). Empty allows any type.
	AllowedTypes []string
}

func (o *Options) defaults() {
	if o.MaxFileSize <= 0 {
		o.MaxFileSize = 10 << 20
	}
	if o.MaxFiles <= 0 {
		o.MaxFiles = 10
	}
	if o.MaxFieldSize <= 0 {
		o.MaxFieldSize = 64 << 10
	}
	if o.MaxFields <= 0 {
		o.MaxFields = 100
	}
}

// File is an uploaded file being streamed. Read it within the callback; it is
// invalid afterwards.
type File struct {
	// Field is the form field name.
	Field string
	// Filename is the sanitized base name supplied by the client.
	Filename string
	// DeclaredType is the Content-Type sent by the client (untrusted).
	DeclaredType string
	// ContentType is sniffed from the file's first bytes.
	ContentType string
	r           *limitedReader
}

// Read reads file data. It fails with a 413 *torge.Error once MaxFileSize is
// exceeded.
func (f *File) Read(p []byte) (int, error) { return f.r.Read(p) }

// Size returns the number of bytes read so far.
func (f *File) Size() int64 { return f.r.n }

// Meta returns storage metadata for the file.
func (f *File) Meta() Meta {
	return Meta{Filename: f.Filename, ContentType: f.ContentType}
}

type limitedReader struct {
	r     io.Reader
	n     int64
	limit int64
	field string
}

func (l *limitedReader) Read(p []byte) (int, error) {
	n, err := l.r.Read(p)
	l.n += int64(n)
	if l.n > l.limit {
		return n, torge.NewError(http.StatusRequestEntityTooLarge, CodeFileTooLarge,
			fmt.Sprintf("File %q exceeds the limit of %d bytes", l.field, l.limit))
	}
	return n, err
}

// Stream processes a multipart/form-data request, calling fn for each file in
// order and returning the non-file fields. Unread file data is discarded.
func Stream(r *http.Request, opts Options, fn func(f *File) error) (url.Values, error) {
	opts.defaults()
	ct := r.Header.Get("Content-Type")
	mediaType, params, err := mime.ParseMediaType(ct)
	if err != nil || mediaType != "multipart/form-data" || params["boundary"] == "" {
		return nil, torge.NewError(http.StatusUnsupportedMediaType, CodeNotMultipart, "Expected a multipart/form-data request")
	}
	mr := multipart.NewReader(r.Body, params["boundary"])
	fields := url.Values{}
	files, nfields := 0, 0
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			return fields, nil
		}
		if err != nil {
			var mbe *http.MaxBytesError
			if errors.As(err, &mbe) {
				return nil, torge.AsError(err)
			}
			return nil, torge.BadRequest(CodeMalformed, "Malformed multipart body").Wrap(err)
		}
		if part.FileName() == "" {
			nfields++
			if nfields > opts.MaxFields {
				return nil, torge.BadRequest(CodeFieldTooLarge, "Too many form fields")
			}
			b, err := io.ReadAll(io.LimitReader(part, opts.MaxFieldSize+1))
			part.Close()
			if err != nil {
				return nil, torge.BadRequest(CodeMalformed, "Malformed multipart body").Wrap(err)
			}
			if int64(len(b)) > opts.MaxFieldSize {
				return nil, torge.NewError(http.StatusRequestEntityTooLarge, CodeFieldTooLarge,
					fmt.Sprintf("Field %q is too large", part.FormName()))
			}
			fields.Add(part.FormName(), string(b))
			continue
		}
		files++
		if files > opts.MaxFiles {
			part.Close()
			return nil, torge.BadRequest(CodeTooManyFiles, fmt.Sprintf("At most %d files are allowed", opts.MaxFiles))
		}
		if err := handlePart(part, opts, fn); err != nil {
			part.Close()
			return nil, err
		}
		part.Close()
	}
}

func handlePart(part *multipart.Part, opts Options, fn func(*File) error) error {
	br := bufio.NewReaderSize(part, 512)
	head, err := br.Peek(512)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, bufio.ErrBufferFull) {
		return torge.BadRequest(CodeMalformed, "Malformed multipart body").Wrap(err)
	}
	sniffed := http.DetectContentType(head)
	if len(opts.AllowedTypes) > 0 && !slices.Contains(opts.AllowedTypes, baseType(sniffed)) {
		return torge.NewError(http.StatusUnsupportedMediaType, CodeTypeNotAllowed,
			fmt.Sprintf("File type %s is not allowed", baseType(sniffed)))
	}
	f := &File{
		Field:        part.FormName(),
		Filename:     SanitizeFilename(part.FileName()),
		DeclaredType: part.Header.Get("Content-Type"),
		ContentType:  sniffed,
		r:            &limitedReader{r: br, limit: opts.MaxFileSize, field: part.FormName()},
	}
	if err := fn(f); err != nil {
		return err
	}
	// Drain the rest so limits are still enforced for unread data.
	if _, err := io.Copy(io.Discard, f); err != nil {
		return err
	}
	return nil
}

func baseType(ct string) string {
	mt, _, err := mime.ParseMediaType(ct)
	if err != nil {
		return ct
	}
	return mt
}

// SanitizeFilename reduces a client-supplied filename to a safe base name.
func SanitizeFilename(name string) string {
	name = strings.ReplaceAll(name, "\\", "/")
	name = path.Base("/" + name)
	name = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || strings.ContainsRune(`<>:"|?*`, r) {
			return -1
		}
		return r
	}, name)
	name = strings.TrimLeft(name, ".")
	if name == "" || name == "/" {
		return "file"
	}
	if len(name) > 255 {
		ext := path.Ext(name)
		if len(ext) > 16 {
			ext = ""
		}
		name = name[:255-len(ext)] + ext
	}
	return name
}

// Meta is metadata stored with an object.
type Meta struct {
	Filename    string
	ContentType string
}

// Object describes a stored object.
type Object struct {
	Key  string
	Size int64
}

// Storage stores uploaded data. Implementations adapt object stores (S3, GCS,
// Azure Blob) or local disks.
type Storage interface {
	Put(ctx context.Context, key string, r io.Reader, meta Meta) (Object, error)
}

// Disk stores objects under a local directory.
type Disk struct {
	// Dir is the root directory. It must exist.
	Dir string
}

// Put writes r to Dir/key. Keys may contain slashes but cannot escape Dir.
// The file is written to a temporary name and renamed, so readers never see
// partial files.
func (d Disk) Put(ctx context.Context, key string, r io.Reader, _ Meta) (Object, error) {
	root, err := os.OpenRoot(d.Dir)
	if err != nil {
		return Object{}, fmt.Errorf("upload: open storage root: %w", err)
	}
	defer root.Close()
	clean := filepath.ToSlash(filepath.Clean(key))
	if dir := filepath.Dir(clean); dir != "." {
		if err := root.MkdirAll(dir, 0o750); err != nil {
			return Object{}, fmt.Errorf("upload: create directory: %w", err)
		}
	}
	tmp := clean + ".partial"
	f, err := root.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o640)
	if err != nil {
		return Object{}, fmt.Errorf("upload: create %q: %w", key, err)
	}
	n, copyErr := io.Copy(f, ctxReader{ctx: ctx, r: r})
	closeErr := f.Close()
	if err := errors.Join(copyErr, closeErr); err != nil {
		_ = root.Remove(tmp)
		return Object{}, err
	}
	if err := root.Rename(tmp, clean); err != nil {
		_ = root.Remove(tmp)
		return Object{}, fmt.Errorf("upload: finalize %q: %w", key, err)
	}
	return Object{Key: clean, Size: n}, nil
}

// ctxReader stops copying when ctx is canceled.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}
