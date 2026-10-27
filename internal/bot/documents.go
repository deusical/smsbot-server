package bot

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// documentText extracts the text of a Word, PowerPoint or Excel document, or
// an OpenDocument one. These are zip files of XML, read with the standard
// library only; formatting, images and comments are left out. What is
// unpacked is capped, so a small file can't expand into a large one.
func documentText(mime string, data []byte) (string, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", errors.New("file_unreadable")
	}
	parts := map[string]*zip.File{}
	for _, f := range zr.File {
		parts[f.Name] = f
	}
	budget := int64(unzipLimit)
	read := func(name string) ([]byte, error) {
		f := parts[name]
		if f == nil {
			return nil, errors.New("file_unreadable")
		}
		if int64(f.UncompressedSize64) > budget {
			return nil, errors.New("file_too_large")
		}
		rc, err := f.Open()
		if err != nil {
			return nil, errors.New("file_unreadable")
		}
		defer rc.Close()
		b, err := io.ReadAll(io.LimitReader(rc, budget+1))
		if err != nil {
			return nil, errors.New("file_unreadable")
		}
		if int64(len(b)) > budget {
			return nil, errors.New("file_too_large")
		}
		budget -= int64(len(b))
		return b, nil
	}
	switch mime {
	case docxType:
		b, err := read("word/document.xml")
		if err != nil {
			return "", err
		}
		return xmlText(b, wordRules), nil
	case pptxType:
		return slidesText(parts, read)
	case xlsxType:
		return spreadsheetText(parts, read)
	}
	b, err := read("content.xml")
	if err != nil {
		return "", err
	}
	return xmlText(b, openDocumentRules), nil
}

// textRules say which parts of an XML document are text. Characters count when
// they are inside an element in text, or anywhere if all is set. start and end
// add a string as an element opens or closes. Elements in skip, with what is
// inside them, are ignored.
type textRules struct {
	text, skip map[string]bool
	all        bool
	start, end map[string]string
}

var (
	wordRules = textRules{
		text:  map[string]bool{"t": true},
		skip:  map[string]bool{"pPr": true, "rPr": true, "sectPr": true},
		start: map[string]string{"tab": "\t", "br": "\n", "cr": "\n"},
		end:   map[string]string{"p": "\n"},
	}
	slideRules = textRules{
		text:  map[string]bool{"t": true},
		start: map[string]string{"br": "\n"},
		end:   map[string]string{"p": "\n"},
	}
	openDocumentRules = textRules{
		all:   true,
		skip:  map[string]bool{"annotation": true},
		start: map[string]string{"tab": "\t", "line-break": "\n", "s": " "},
		end:   map[string]string{"p": "\n", "h": "\n", "table-row": "\n", "table-cell": "\t"},
	}
)

func xmlText(b []byte, r textRules) string {
	d := xml.NewDecoder(bytes.NewReader(b))
	var out strings.Builder
	inText, skipping := 0, 0
	for out.Len() < fileTextChars*4 {
		tok, err := d.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			name := t.Name.Local
			switch {
			case skipping > 0:
				skipping++
			case r.skip[name]:
				skipping = 1
			default:
				if r.text[name] {
					inText++
				}
				out.WriteString(r.start[name])
			}
		case xml.EndElement:
			name := t.Name.Local
			switch {
			case skipping > 0:
				skipping--
			default:
				if r.text[name] {
					inText--
				}
				out.WriteString(r.end[name])
			}
		case xml.CharData:
			if skipping == 0 && (r.all || inText > 0) {
				out.Write(t)
			}
		}
	}
	return out.String()
}

var slideName = regexp.MustCompile(`^ppt/slides/slide(\d+)\.xml$`)

func slidesText(parts map[string]*zip.File, read func(string) ([]byte, error)) (string, error) {
	type slide struct {
		n    int
		name string
	}
	var slides []slide
	for name := range parts {
		if m := slideName.FindStringSubmatch(name); m != nil {
			n, _ := strconv.Atoi(m[1])
			slides = append(slides, slide{n, name})
		}
	}
	if len(slides) == 0 {
		return "", errors.New("file_unreadable")
	}
	sort.Slice(slides, func(i, j int) bool { return slides[i].n < slides[j].n })
	var out strings.Builder
	for _, s := range slides {
		b, err := read(s.name)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&out, "Slide %d:\n%s\n\n", s.n, xmlText(b, slideRules))
		if out.Len() > fileTextChars*2 {
			break
		}
	}
	return out.String(), nil
}

// xmlAttr is the value of an attribute by its local name.
func xmlAttr(e xml.StartElement, name string) string {
	for _, a := range e.Attr {
		if a.Name.Local == name {
			return a.Value
		}
	}
	return ""
}

// sharedStrings reads an Excel workbook's table of text values.
func sharedStrings(b []byte) []string {
	d := xml.NewDecoder(bytes.NewReader(b))
	var all []string
	var cur strings.Builder
	inT, phonetic := false, 0
	for {
		tok, err := d.Token()
		if err != nil {
			return all
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "si":
				cur.Reset()
			case "t":
				inT = phonetic == 0
			case "rPh":
				phonetic++
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "si":
				all = append(all, cur.String())
			case "t":
				inT = false
			case "rPh":
				phonetic--
			}
		case xml.CharData:
			if inT {
				cur.Write(t)
			}
		}
	}
}

// columnIndex turns a cell reference such as "C7" into its zero-based column.
func columnIndex(ref string) int {
	n := 0
	for _, r := range ref {
		if r < 'A' || r > 'Z' {
			break
		}
		n = n*26 + int(r-'A') + 1
	}
	return n - 1
}

// sheetFiles lists a workbook's sheets in order, as names and part paths.
func sheetFiles(parts map[string]*zip.File, read func(string) ([]byte, error)) (names, files []string) {
	book, err := read("xl/workbook.xml")
	rels, err2 := read("xl/_rels/workbook.xml.rels")
	if err == nil && err2 == nil {
		targets := map[string]string{}
		d := xml.NewDecoder(bytes.NewReader(rels))
		for {
			tok, err := d.Token()
			if err != nil {
				break
			}
			if e, ok := tok.(xml.StartElement); ok && e.Name.Local == "Relationship" {
				target := xmlAttr(e, "Target")
				if strings.HasPrefix(target, "/") {
					target = strings.TrimPrefix(target, "/")
				} else {
					target = path.Join("xl", target)
				}
				targets[xmlAttr(e, "Id")] = target
			}
		}
		d = xml.NewDecoder(bytes.NewReader(book))
		for {
			tok, err := d.Token()
			if err != nil {
				break
			}
			if e, ok := tok.(xml.StartElement); ok && e.Name.Local == "sheet" {
				if file := targets[xmlAttr(e, "id")]; parts[file] != nil {
					names, files = append(names, xmlAttr(e, "name")), append(files, file)
				}
			}
		}
	}
	if len(files) == 0 {
		for name := range parts {
			if strings.HasPrefix(name, "xl/worksheets/sheet") && strings.HasSuffix(name, ".xml") {
				files = append(files, name)
			}
		}
		sort.Strings(files)
		for i := range files {
			names = append(names, fmt.Sprintf("Sheet %d", i+1))
		}
	}
	return names, files
}

func spreadsheetText(parts map[string]*zip.File, read func(string) ([]byte, error)) (string, error) {
	var shared []string
	if b, err := read("xl/sharedStrings.xml"); err == nil {
		shared = sharedStrings(b)
	}
	names, files := sheetFiles(parts, read)
	if len(files) == 0 {
		return "", errors.New("file_unreadable")
	}
	var out strings.Builder
	for i, file := range files {
		b, err := read(file)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&out, "Sheet %q:\n", names[i])
		out.WriteString(sheetRows(b, shared))
		out.WriteString("\n")
		if out.Len() > fileTextChars*2 {
			break
		}
	}
	return out.String(), nil
}

// sheetRows writes one worksheet's rows, one per line with cells separated by
// tabs. Formulas show the value they last had.
func sheetRows(b []byte, shared []string) string {
	d := xml.NewDecoder(bytes.NewReader(b))
	var out strings.Builder
	var row []string
	var kind, value string
	col, inV, inText := 0, false, false
	for out.Len() < fileTextChars*2 {
		tok, err := d.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "row":
				row = row[:0]
			case "c":
				kind, value = xmlAttr(t, "t"), ""
				if ref := xmlAttr(t, "r"); ref != "" {
					col = columnIndex(ref)
				} else {
					col = len(row)
				}
			case "v":
				inV = true
			case "t":
				inText = true
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "v":
				inV = false
			case "t":
				inText = false
			case "c":
				text := value
				switch kind {
				case "s":
					if n, err := strconv.Atoi(value); err == nil && n >= 0 && n < len(shared) {
						text = shared[n]
					} else {
						text = ""
					}
				case "b":
					text = map[string]string{"1": "TRUE", "0": "FALSE"}[value]
				}
				for len(row) <= col && col < 1000 {
					row = append(row, "")
				}
				if col < len(row) {
					row[col] = strings.NewReplacer("\t", " ", "\n", " ").Replace(text)
				}
			case "row":
				if line := strings.TrimRight(strings.Join(row, "\t"), "\t"); line != "" {
					out.WriteString(line + "\n")
				}
			}
		case xml.CharData:
			// A value is in <v>; text typed straight into a cell is in <is><t>.
			if inV || inText {
				value += string(t)
			}
		}
	}
	return out.String()
}
