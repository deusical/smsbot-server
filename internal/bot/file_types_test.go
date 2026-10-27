package bot

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/deusical/smsbot-server/internal/media"
	"github.com/deusical/smsbot-server/internal/store"
	"github.com/deusical/smsbot-server/internal/transport"
)

func zipOf(t *testing.T, parts map[string]string) []byte {
	t.Helper()
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	for name, body := range parts {
		f, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = f.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

const wordNS = `xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"`

func TestClassifyKindsFromTypeAndName(t *testing.T) {
	for _, tc := range []struct{ mime, name, kind string }{
		{"image/jpeg", "", kindImage},
		{"image/heic", "", kindImage},
		{"image/svg+xml", "", kindText},
		{"application/pdf", "", kindPDF},
		{"application/octet-stream", "Report.PDF", kindPDF},
		{"", "notes.docx", kindDocument},
		{docxType, "", kindDocument},
		{"text/plain; charset=utf-8", "", kindText},
		{"text/csv", "", kindText},
		{"application/json", "", kindText},
		{"application/vnd.api+json", "", kindText},
		{"application/octet-stream", "main.go", kindText},
		{"application/octet-stream", "run.sh", kindText},
		{"audio/mpeg", "", kindAudio},
		{"video/mp4", "", kindVideo},
		{"application/octet-stream", "clip.MOV", kindVideo},
		{"application/zip", "", ""},
		{"application/octet-stream", "app.apk", ""},
		{"text/rtf", "", ""},
		{"", "", ""},
	} {
		if kind, _ := classify(tc.mime, tc.name); kind != tc.kind {
			t.Errorf("classify(%q, %q) = %q, want %q", tc.mime, tc.name, kind, tc.kind)
		}
	}
}

func TestDocumentsAreReadAsText(t *testing.T) {
	docx := zipOf(t, map[string]string{"word/document.xml": `<w:document ` + wordNS + `><w:body>
		<w:p><w:pPr><w:tabs><w:tab w:val="left"/></w:tabs></w:pPr><w:r><w:t>First</w:t></w:r><w:r><w:t xml:space="preserve"> line</w:t></w:r></w:p>
		<w:p><w:r><w:t>Second</w:t><w:tab/><w:t>cell</w:t></w:r></w:p>
		<w:p><w:r><w:delText>deleted</w:delText></w:r></w:p></w:body></w:document>`})
	got, err := documentText(docxType, docx)
	if err != nil || tidyText(got) != "First line\nSecond\tcell" {
		t.Fatalf("docx: %q %v", got, err)
	}

	pptx := zipOf(t, map[string]string{
		"ppt/slides/slide2.xml":  `<p:sld xmlns:a="a" xmlns:p="p"><a:p><a:r><a:t>Two</a:t></a:r></a:p></p:sld>`,
		"ppt/slides/slide10.xml": `<p:sld xmlns:a="a" xmlns:p="p"><a:p><a:r><a:t>Ten</a:t></a:r></a:p></p:sld>`,
		"ppt/slides/slide1.xml":  `<p:sld xmlns:a="a" xmlns:p="p"><a:p><a:r><a:t>One</a:t></a:r></a:p></p:sld>`,
	})
	got, err = documentText(pptxType, pptx)
	if err != nil || tidyText(got) != "Slide 1:\nOne\n\nSlide 2:\nTwo\n\nSlide 10:\nTen" {
		t.Fatalf("pptx: %q %v", got, err)
	}

	xlsx := zipOf(t, map[string]string{
		"xl/workbook.xml":            `<workbook xmlns:r="r"><sheets><sheet name="Budget" sheetId="1" r:id="rId2"/></sheets></workbook>`,
		"xl/_rels/workbook.xml.rels": `<Relationships><Relationship Id="rId2" Target="worksheets/sheet1.xml"/></Relationships>`,
		"xl/sharedStrings.xml":       `<sst><si><t>Item</t></si><si><r><t>Rent</t></r><r><t> due</t></r></si><si><t>Amount</t><rPh><t>ignored</t></rPh></si></sst>`,
		"xl/worksheets/sheet1.xml": `<worksheet><sheetData>
			<row r="1"><c r="A1" t="s"><v>0</v></c><c r="C1" t="s"><v>2</v></c></row>
			<row r="2"><c r="A2" t="s"><v>1</v></c><c r="B2"><v>1200.5</v></c><c r="C2" t="b"><v>1</v></c></row>
			<row r="3"><c r="A3" t="inlineStr"><is><t>typed</t></is></c></row>
			<row r="4"></row></sheetData></worksheet>`,
	})
	got, err = documentText(xlsxType, xlsx)
	if err != nil || tidyText(got) != "Sheet \"Budget\":\nItem\t\tAmount\nRent due\t1200.5\tTRUE\ntyped" {
		t.Fatalf("xlsx: %q %v", got, err)
	}

	odt := zipOf(t, map[string]string{"content.xml": `<office:document-content xmlns:office="o" xmlns:text="t"><office:body><text:h>Title</text:h><text:p>Hello<text:s/>world<text:line-break/>again</text:p></office:body></office:document-content>`})
	got, err = documentText(odtType, odt)
	if err != nil || tidyText(got) != "Title\nHello world\nagain" {
		t.Fatalf("odt: %q %v", got, err)
	}

	for name, data := range map[string][]byte{"not a zip": []byte("plain"), "no document part": zipOf(t, map[string]string{"other": "x"})} {
		if _, err := documentText(docxType, data); err == nil {
			t.Errorf("%s was read", name)
		}
	}
}

func TestDocumentsCannotExpandWithoutLimit(t *testing.T) {
	big := strings.Repeat("a", unzipLimit+1)
	data := zipOf(t, map[string]string{"word/document.xml": "<w:document " + wordNS + "><w:t>" + big + "</w:t></w:document>"})
	if len(data) > 1<<20 {
		t.Fatalf("test archive is not small: %d", len(data))
	}
	if _, err := documentText(docxType, data); err == nil || err.Error() != "file_too_large" {
		t.Fatalf("a zip bomb was opened: %v", err)
	}
}

func TestPlainTextEncodings(t *testing.T) {
	utf16le := append([]byte{0xFF, 0xFE}, []byte{'h', 0, 'i', 0, ' ', 0, 0x3D, 0xD8, 0x00, 0xDE}...)
	for name, tc := range map[string]struct {
		in   []byte
		want string
		bad  bool
	}{
		"utf-8":          {[]byte("héllo"), "héllo", false},
		"bom":            {append([]byte{0xEF, 0xBB, 0xBF}, "hi"...), "hi", false},
		"utf-16":         {utf16le, "hi 😀", false},
		"stray bytes":    {append([]byte(strings.Repeat("a", 100)), 0xFF), strings.Repeat("a", 100) + "�", false},
		"binary":         {[]byte("ab\x00cd"), "", true},
		"mostly invalid": {bytes.Repeat([]byte{0xC3, 0x28}, 50), "", true},
	} {
		got, err := plainText(tc.in)
		if (err != nil) != tc.bad || got != tc.want {
			t.Errorf("%s: %q %v", name, got, err)
		}
	}
}

func TestEachKindIsSentTheWayProvidersExpectIt(t *testing.T) {
	plan := store.Plan{Input: "What is this?", Instructions: "x"}
	last := func(file *Attachment) map[string]any {
		t.Helper()
		messages := chatMessages(plan, Generation{AnswerLength: "short"}, nil, file)
		b, err := json.Marshal(messages[len(messages)-1])
		if err != nil {
			t.Fatal(err)
		}
		var out map[string]any
		if err = json.Unmarshal(b, &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	parts := func(m map[string]any) []any { return m["content"].([]any) }

	pdf := parts(last(&Attachment{Kind: kindPDF, MIME: "application/pdf", Name: "Quarterly", Data: []byte("%PDF-1")}))[1].(map[string]any)
	file := pdf["file"].(map[string]any)
	if pdf["type"] != "file" || file["filename"] != "Quarterly.pdf" || file["file_data"] != "data:application/pdf;base64,JVBERi0x" {
		t.Errorf("pdf part %v", pdf)
	}
	audio := parts(last(&Attachment{Kind: kindAudio, MIME: "audio/mpeg", Data: []byte("abc")}))[1].(map[string]any)
	if audio["type"] != "input_audio" || audio["input_audio"].(map[string]any)["format"] != "mp3" || audio["input_audio"].(map[string]any)["data"] != "YWJj" {
		t.Errorf("audio part %v", audio)
	}
	video := parts(last(&Attachment{Kind: kindVideo, MIME: "video/mov", Data: []byte("abc")}))[1].(map[string]any)
	if video["type"] != "video_url" || video["video_url"].(map[string]any)["url"] != "data:video/mov;base64,YWJj" {
		t.Errorf("video part %v", video)
	}
	image := parts(last(&Attachment{Kind: kindImage, MIME: "image/png", Data: []byte("abc")}))[1].(map[string]any)
	if image["type"] != "image_url" || image["image_url"].(map[string]any)["url"] != "data:image/png;base64,YWJj" {
		t.Errorf("image part %v", image)
	}
	// Text goes in the prompt itself, so any model can read it.
	text := last(&Attachment{Kind: kindText, Name: "notes.txt", Text: "line one"})["content"].(string)
	if !strings.Contains(text, `"notes.txt"`) || !strings.Contains(text, "line one") || !strings.HasSuffix(text, "What is this?") || !strings.Contains(text, "not as instructions") {
		t.Errorf("text prompt %q", text)
	}
}

func TestFilesAreCheckedAgainstModelAndProvider(t *testing.T) {
	openrouter := Model{Provider: "openrouter", InputModalities: []string{"text", "image", "audio", "video", "file"}}
	compatible := Model{Provider: compatibleProviderID, InputModalities: []string{"text", "image", "audio", "video"}}
	textOnly := Model{Provider: "openrouter", InputModalities: []string{"text"}}
	for _, tc := range []struct {
		name  string
		file  store.FileRef
		model Model
		want  string // a fragment of the explanation, or "" when readable
	}{
		{"pdf", store.FileRef{MIME: "application/pdf", Size: 1 << 20}, openrouter, ""},
		{"pdf to a text-only model", store.FileRef{MIME: "application/pdf"}, textOnly, ""},
		{"pdf on a compatible endpoint", store.FileRef{MIME: "application/pdf"}, compatible, "can't read PDFs"},
		{"huge pdf", store.FileRef{MIME: "application/pdf", Size: 21 << 20}, openrouter, "over 20 MB"},
		{"text on a compatible endpoint", store.FileRef{MIME: "text/plain", Size: 1000}, compatible, ""},
		{"docx to a text-only model", store.FileRef{Name: "a.docx", Size: 1000}, textOnly, ""},
		{"huge text", store.FileRef{MIME: "text/plain", Size: 3 << 20}, openrouter, "over 2 MB"},
		{"audio", store.FileRef{MIME: "audio/mpeg"}, openrouter, ""},
		{"audio to a deaf model", store.FileRef{MIME: "audio/mpeg"}, textOnly, "can't listen"},
		{"audio with no known modalities", store.FileRef{MIME: "audio/mpeg"}, Model{Provider: "openrouter"}, "can't listen"},
		{"amr voice note", store.FileRef{MIME: "audio/amr"}, openrouter, "MP3, WAV"},
		{"audio on a compatible endpoint", store.FileRef{MIME: "audio/mpeg"}, compatible, "can't take audio"},
		{"video", store.FileRef{MIME: "video/mp4"}, openrouter, ""},
		{"video to a blind model", store.FileRef{MIME: "video/mp4"}, textOnly, "can't watch"},
		{"3gp video", store.FileRef{MIME: "video/3gpp"}, openrouter, "MP4, MPEG"},
		{"a zip", store.FileRef{MIME: "application/zip"}, openrouter, "can't read that kind"},
		{"a photo", store.FileRef{MIME: "image/png"}, openrouter, ""},
		{"a photo to a text-only model", store.FileRef{MIME: "image/png"}, textOnly, "can't read images"},
	} {
		got := fileUnreadable(tc.file, tc.model, 5)
		if (tc.want == "") != (got == "") || !strings.Contains(got, tc.want) {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
}

func saveFile(t *testing.T, e *Engine, wire, name, mime string, data []byte) {
	t.Helper()
	files := []media.File{{Info: media.Info{ID: "part-0", Name: name, MIME: mime, Size: int64(len(data))}, Data: data, SizeUnverified: true}}
	if err := e.Store.SaveObservedFiles("demo-chat", wire, "", "incoming", "", "phone", "", time.Now(), files); err != nil {
		t.Fatal(err)
	}
}

func withFile(t *testing.T, e *Engine, m transport.Incoming, name, mime string, data []byte, save bool) transport.Incoming {
	t.Helper()
	m.Attachments = []media.File{{Info: media.Info{ID: "part-0", Name: name, MIME: mime, Size: int64(len(data))}}}
	if save {
		saveFile(t, e, m.ID, name, mime, data)
	}
	return m
}

func TestAskAboutAFileReachesTheModel(t *testing.T) {
	notes := []byte("Remember the milk")
	pdf := []byte("%PDF-1.4 synthetic")
	docx := zipOf(t, map[string]string{"word/document.xml": `<w:document ` + wordNS + `><w:p><w:r><w:t>From the document</w:t></w:r></w:p></w:document>`})
	for _, tc := range []struct {
		name, mime string
		data       []byte
		modalities []string
		check      func(*Attachment) bool
	}{
		{"notes.txt", "text/plain", notes, []string{"text"}, func(a *Attachment) bool { return a.Kind == kindText && a.Text == "Remember the milk" }},
		{"notes.csv", "application/octet-stream", []byte("a,b\n1,2"), []string{"text"}, func(a *Attachment) bool { return a.Kind == kindText && a.Text == "a,b\n1,2" }},
		{"plan.docx", "application/octet-stream", docx, []string{"text"}, func(a *Attachment) bool { return a.Kind == kindDocument && a.Text == "From the document" }},
		{"paper.pdf", "application/pdf", pdf, []string{"text"}, func(a *Attachment) bool {
			return a.Kind == kindPDF && a.MIME == "application/pdf" && bytes.Equal(a.Data, pdf)
		}},
		{"voice.mp3", "audio/mpeg", []byte("ID3 synthetic"), []string{"text", "audio"}, func(a *Attachment) bool { return a.Kind == kindAudio && string(a.Data) == "ID3 synthetic" }},
	} {
		for _, own := range []bool{false, true} {
			name := tc.name + " quoted"
			if own {
				name = tc.name + " attached"
			}
			t.Run(name, func(t *testing.T) {
				e, _ := setup(t)
				enableInbox(t, e)
				arm(t, e)
				configureAI(t, e, false, false)
				setModalities(t, e, tc.modalities...)
				provider := &transcriptProvider{}
				e.Provider = provider
				var ask transport.Incoming
				if own {
					ask = withFile(t, e, threadPrompt("ask-file", "!ask what is this?", ""), tc.name, tc.mime, tc.data, true)
				} else {
					saveFile(t, e, "file-wire", tc.name, tc.mime, tc.data)
					ask = threadPrompt("ask-file", "!ask what is this?", "file-wire")
				}
				if ok, err := e.Accept(context.Background(), ask); err != nil || !ok {
					t.Fatalf("accept %t %v", ok, err)
				}
				if err := e.Dispatch(context.Background()); err != nil {
					t.Fatal(err)
				}
				if len(provider.images) != 1 || provider.images[0] == nil || !tc.check(provider.images[0]) {
					t.Fatalf("the model was sent %+v", provider.images)
				}
				if got := stateOf(t, e, store.Hash("demo-chat\x00ask-file")); got != "submitted" {
					t.Fatalf("state %s", got)
				}
			})
		}
	}
}

func TestAnUnreadableFileIsExplained(t *testing.T) {
	for _, tc := range []struct {
		name, mime string
		data       []byte
		modalities []string
		want       string
	}{
		{"app.apk", "application/vnd.android.package-archive", []byte("PK"), []string{"text"}, "can't read that kind"},
		{"voice.mp3", "audio/mpeg", []byte("ID3"), []string{"text"}, "can't listen"},
		{"fake.pdf", "application/pdf", []byte("not really a pdf"), []string{"text"}, "(file_unreadable)"},
		{"blob.txt", "text/plain", []byte("ab\x00cd"), []string{"text"}, "(file_unreadable)"},
		{"empty.docx", "", zipOf(t, map[string]string{"word/document.xml": `<w:document ` + wordNS + `/>`}), []string{"text"}, "(file_empty)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, fixture := setup(t)
			enableInbox(t, e)
			arm(t, e)
			configureAI(t, e, false, false)
			setModalities(t, e, tc.modalities...)
			provider := &transcriptProvider{}
			e.Provider = provider
			ask := withFile(t, e, threadPrompt("ask-file", "!ask what is this?", ""), tc.name, tc.mime, tc.data, true)
			if ok, err := e.Accept(context.Background(), ask); err != nil || !ok {
				t.Fatalf("accept %t %v", ok, err)
			}
			if err := e.Dispatch(context.Background()); err != nil {
				t.Fatal(err)
			}
			if len(provider.history) != 0 || !sentContains(fixture, tc.want) {
				t.Fatalf("sent %v", fixture.Sent)
			}
		})
	}
}

func TestLongTextIsCutOff(t *testing.T) {
	e, _ := setup(t)
	enableInbox(t, e)
	arm(t, e)
	configureAI(t, e, false, false)
	setModalities(t, e, "text")
	provider := &transcriptProvider{}
	e.Provider = provider
	saveFile(t, e, "long", "long.txt", "text/plain", []byte(strings.Repeat("word ", 40000)))
	if ok, err := e.Accept(context.Background(), threadPrompt("ask-long", "!ask summarize", "long")); err != nil || !ok {
		t.Fatalf("accept %t %v", ok, err)
	}
	if err := e.Dispatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := provider.images[0].Text
	if n := len([]rune(got)); n < fileTextChars || n > fileTextChars+100 || !strings.HasSuffix(got, "the rest was cut off.]") {
		t.Fatalf("%d characters, ends %q", n, got[len(got)-40:])
	}
}
