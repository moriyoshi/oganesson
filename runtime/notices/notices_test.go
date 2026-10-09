package notices

import (
	"strings"
	"testing"
	"testing/fstest"
)

func TestWrite(t *testing.T) {
	var b strings.Builder
	if err := Write(&b, nil); err != nil || b.String() != Text {
		t.Errorf("Write(nil) = %d bytes, %v; want Text", b.Len(), err)
	}
	b.Reset()
	if err := Write(&b, fstest.MapFS{"server.js": {}}); err != nil || b.String() != Text {
		t.Errorf("Write(app without %s) = %d bytes, %v; want Text", AppFile, b.Len(), err)
	}
	b.Reset()
	app := fstest.MapFS{AppFile: {Data: []byte("npm packages\n")}}
	if err := Write(&b, app); err != nil {
		t.Fatal(err)
	}
	if want := Text + "\n" + separator + "\n\nnpm packages\n"; b.String() != want {
		t.Errorf("Write(app) ends %q", b.String()[len(Text):])
	}
}
