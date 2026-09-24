package upload_test

import (
	"bytes"
	"context"
	"io"
	"mime/multipart"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/TosmimForidMehtab/torge"
	"github.com/TosmimForidMehtab/torge/torgetest"
	"github.com/TosmimForidMehtab/torge/upload"
)

var pngHeader = []byte("\x89PNG\r\n\x1a\n")

func multipartBody(t *testing.T, fields map[string]string, files map[string][]byte) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	for k, v := range fields {
		_ = w.WriteField(k, v)
	}
	for name, data := range files {
		fw, err := w.CreateFormFile("file", name)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = fw.Write(data)
	}
	_ = w.Close()
	return &buf, w.FormDataContentType()
}

func TestStreamUpload(t *testing.T) {
	dir := t.TempDir()
	storage := upload.Disk{Dir: dir}
	app := torgetest.NewApp(t)
	app.POST("/upload", func(c *torge.Context) error {
		var stored []string
		fields, err := upload.Stream(c.Request(), upload.Options{
			MaxFileSize:  1024,
			AllowedTypes: []string{"image/png"},
		}, func(f *upload.File) error {
			obj, err := storage.Put(c.Context(), "avatars/"+f.Filename, f, f.Meta())
			if err != nil {
				return err
			}
			stored = append(stored, obj.Key+":"+f.ContentType)
			return nil
		})
		if err != nil {
			return err
		}
		return c.JSON(200, map[string]any{"title": fields.Get("title"), "stored": stored})
	}, torge.BodyLimit(10<<20))
	tc := torgetest.New(t, app)

	png := append(append([]byte{}, pngHeader...), bytes.Repeat([]byte{1}, 100)...)
	body, ct := multipartBody(t, map[string]string{"title": "me"}, map[string][]byte{"../../evil.png": png})
	tc.POST("/upload").Body(body, ct).Do().ExpectStatus(200).
		ExpectJSONPath("title", "me").ExpectJSONPath("stored.0", "avatars/evil.png:image/png")
	if data, err := os.ReadFile(filepath.Join(dir, "avatars", "evil.png")); err != nil || !bytes.Equal(data, png) {
		t.Fatalf("stored file mismatch: %v", err)
	}

	big := append(append([]byte{}, pngHeader...), bytes.Repeat([]byte{1}, 2048)...)
	body, ct = multipartBody(t, nil, map[string][]byte{"big.png": big})
	tc.POST("/upload").Body(body, ct).Do().ExpectStatus(413).ExpectErrorCode(upload.CodeFileTooLarge)
	if _, err := os.Stat(filepath.Join(dir, "avatars", "big.png")); !os.IsNotExist(err) {
		t.Fatal("partial files must not be left behind")
	}

	body, ct = multipartBody(t, nil, map[string][]byte{"script.png": []byte("<html><script>")})
	tc.POST("/upload").Body(body, ct).Do().ExpectStatus(415).ExpectErrorCode(upload.CodeTypeNotAllowed)

	tc.POST("/upload").JSON(map[string]string{}).Do().ExpectStatus(415).ExpectErrorCode(upload.CodeNotMultipart)
}

func TestSanitizeFilename(t *testing.T) {
	for in, want := range map[string]string{
		"../../etc/passwd":    "passwd",
		`C:\Windows\evil.exe`: "evil.exe",
		".hidden":             "hidden",
		"a<b>c.txt":           "abc.txt",
		"":                    "file",
		"..":                  "file",
	} {
		if got := upload.SanitizeFilename(in); got != want {
			t.Errorf("SanitizeFilename(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDiskRejectsEscapes(t *testing.T) {
	d := upload.Disk{Dir: t.TempDir()}
	if _, err := d.Put(context.Background(), "../outside.txt", strings.NewReader("x"), upload.Meta{}); err == nil {
		t.Fatal("keys must not escape the storage root")
	}
	obj, err := d.Put(context.Background(), "a/b.txt", io.LimitReader(strings.NewReader("hello"), 5), upload.Meta{})
	if err != nil || obj.Size != 5 {
		t.Fatalf("put: %+v %v", obj, err)
	}
}
