package media

import "testing"

func TestUnlimitedAndIndependentAttachmentLimits(t *testing.T) {
	data := make([]byte, 11<<20)
	uploads := []Upload{{Name: "large-a.bin", Data: data}, {Name: "large-b.bin", Data: data}, {Data: []byte{1}}, {Data: []byte{2}}, {Data: []byte{3}}}
	files, err := PrepareWithLimits(uploads, Limits{})
	if err != nil || len(files) != 5 {
		t.Fatal("unlimited retained old attachment caps", err)
	}
	for _, limits := range []Limits{{MaxFiles: 4}, {MaxFileMiB: 10}, {MaxTotalMiB: 20}} {
		if _, err := PrepareWithLimits(uploads, limits); err == nil {
			t.Fatal("configured finite limit ignored", limits)
		}
	}
	if (Limits{}).RequestBytes() != int64(^uint64(0)>>1) {
		t.Fatal("unlimited retained a request body cap")
	}
	for _, limits := range []Limits{{MaxFiles: -1}, {MaxFileMiB: -1}, {MaxTotalMiB: -1}, {MaxFileMiB: 1 << 50}} {
		if limits.Validate() == nil {
			t.Fatal("invalid limit accepted")
		}
	}
}

func TestUploadLimitsAndIntentBindsBytes(t *testing.T) {
	for _, uploads := range [][]Upload{
		{{Name: "empty"}},
		{{Name: "too-large", Data: make([]byte, MaxFileBytes+1)}},
		{{Data: []byte{1}}, {Data: []byte{1}}, {Data: []byte{1}}, {Data: []byte{1}}, {Data: []byte{1}}},
		{{Data: make([]byte, MaxFileBytes)}, {Data: make([]byte, MaxFileBytes)}, {Data: []byte{1}}},
	} {
		if _, err := Prepare(uploads); err == nil {
			t.Fatal("invalid upload accepted")
		}
	}
	a, err := Prepare([]Upload{{Name: "../synthetic.txt", MIME: "text/plain; charset=utf-8", Data: []byte("one")}})
	if err != nil || a[0].Name != "synthetic.txt" || a[0].MIME != "text/plain" {
		t.Fatal("upload normalization failed", err)
	}
	b, _ := Prepare([]Upload{{Name: "../synthetic.txt", MIME: "text/plain", Data: []byte("two")}})
	if Intent("", a) == Intent("", b) {
		t.Fatal("compose intent does not bind bytes")
	}
}

func TestUnlimitedFileIDsDoNotWrapAfter256Files(t *testing.T) {
	uploads := make([]Upload, 257)
	for i := range uploads {
		uploads[i] = Upload{Name: "synthetic.txt", Data: []byte("Synthetic identical bytes")}
	}
	files, err := PrepareWithLimits(uploads, Limits{})
	if err != nil || files[0].ID == files[256].ID {
		t.Fatal("unlimited attachment IDs collide", err)
	}
}
