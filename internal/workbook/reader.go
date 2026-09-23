// Package workbook converts the supplied IEK and Systeme Electric XLSX exports
// into a validated planning snapshot. It reads cached values without executing
// formulas, extracting archives, or contacting external workbook relationships.
package workbook

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"path"
	"strconv"
	"strings"
)

const (
	maxExpandedBytes = 512 << 20
	maxEntries       = 4096
	maxRows          = 1_000_000
	maxCells         = 12_000_000
	maxStrings       = 1_000_000
	maxTextBytes     = 64 << 20
	maxCellBytes     = 64 << 10
)

type budget struct {
	expanded, rows, cells, strings, text int64
}

type sheet struct{ name, target string }
type book struct {
	ctx     context.Context
	files   map[string]*zip.File
	strings []string
	sheets  []sheet
	budget  *budget
}

func openBook(ctx context.Context, data []byte, limits *budget) (*book, error) {
	z, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("invalid XLSX ZIP archive: %w", err)
	}
	if len(z.File) > maxEntries {
		return nil, fmt.Errorf("workbook exceeds %d ZIP entries", maxEntries)
	}
	b := &book{ctx: ctx, budget: limits, files: make(map[string]*zip.File, len(z.File))}
	for _, file := range z.File {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		name := file.Name
		if strings.Contains(name, "\\") || strings.HasPrefix(name, "/") || path.Clean(name) != strings.TrimSuffix(name, "/") || strings.HasPrefix(name, "../") {
			return nil, fmt.Errorf("unsafe ZIP entry %q", name)
		}
		if _, exists := b.files[name]; exists {
			return nil, fmt.Errorf("duplicate ZIP entry %q", name)
		}
		if strings.Contains(strings.ToLower(name), "vbaproject") {
			return nil, fmt.Errorf("macro-enabled workbooks are not supported")
		}
		if file.UncompressedSize64 > maxExpandedBytes || uint64(limits.expanded)+file.UncompressedSize64 > maxExpandedBytes {
			return nil, fmt.Errorf("uploaded workbooks exceed 512 MiB expanded")
		}
		limits.expanded += int64(file.UncompressedSize64)
		b.files[name] = file
	}
	rels := map[string]string{}
	err = b.tokens("xl/_rels/workbook.xml.rels", func(token xml.Token) error {
		start, ok := token.(xml.StartElement)
		if !ok || start.Name.Local != "Relationship" {
			return nil
		}
		id, target := attr(start, "Id"), attr(start, "Target")
		if attr(start, "TargetMode") == "External" {
			return fmt.Errorf("external workbook relationships are not supported")
		}
		if id == "" || target == "" || strings.Contains(target, "\\") || strings.Contains(target, ":") {
			return fmt.Errorf("invalid workbook relationship")
		}
		for _, part := range strings.Split(target, "/") {
			if part == ".." {
				return fmt.Errorf("unsafe workbook relationship")
			}
		}
		if strings.HasPrefix(target, "/") {
			target = strings.TrimPrefix(target, "/")
		} else {
			target = "xl/" + target
		}
		if _, exists := rels[id]; exists {
			return fmt.Errorf("duplicate workbook relationship %q", id)
		}
		rels[id] = target
		return nil
	})
	if err != nil {
		return nil, err
	}
	err = b.tokens("xl/workbook.xml", func(token xml.Token) error {
		start, ok := token.(xml.StartElement)
		if !ok || start.Name.Local != "sheet" {
			return nil
		}
		target := rels[attr(start, "id")]
		if target == "" || b.files[target] == nil || len(b.sheets) >= 32 {
			return fmt.Errorf("missing sheet relationship or too many worksheets")
		}
		b.sheets = append(b.sheets, sheet{attr(start, "name"), target})
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(b.sheets) == 0 {
		return nil, fmt.Errorf("workbook has no worksheets")
	}
	if b.files["xl/sharedStrings.xml"] != nil {
		var value strings.Builder
		inString, inText := false, false
		err = b.tokens("xl/sharedStrings.xml", func(token xml.Token) error {
			switch t := token.(type) {
			case xml.StartElement:
				if t.Name.Local == "si" {
					inString = true
					value.Reset()
				}
				if t.Name.Local == "t" && inString {
					inText = true
				}
			case xml.CharData:
				if inText {
					if value.Len()+len(t) > maxCellBytes {
						return fmt.Errorf("shared string exceeds 64 KiB")
					}
					value.Write(t)
				}
			case xml.EndElement:
				if t.Name.Local == "t" {
					inText = false
				}
				if t.Name.Local == "si" {
					limits.strings++
					limits.text += int64(value.Len())
					if limits.strings > maxStrings || limits.text > maxTextBytes {
						return fmt.Errorf("workbooks contain too many shared strings")
					}
					b.strings = append(b.strings, value.String())
					inString = false
				}
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return b, nil
}

func attr(start xml.StartElement, name string) string {
	for _, a := range start.Attr {
		if a.Name.Local == name {
			return a.Value
		}
	}
	return ""
}

func (b *book) tokens(name string, visit func(xml.Token) error) error {
	file := b.files[name]
	if file == nil {
		return fmt.Errorf("missing XLSX part %q", name)
	}
	r, err := file.Open()
	if err != nil {
		return err
	}
	defer r.Close()
	d := xml.NewDecoder(io.LimitReader(r, int64(file.UncompressedSize64)+1))
	for {
		if err := b.ctx.Err(); err != nil {
			return err
		}
		token, err := d.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("%s: invalid XML: %w", name, err)
		}
		if _, forbidden := token.(xml.Directive); forbidden {
			return fmt.Errorf("%s: XML declarations of DTDs/entities are forbidden", name)
		}
		if err := visit(token); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
}

type row map[string]string

// rows streams nonempty rows, retaining Excel's sparse column references. The
// ordinal matches the original converter's nonempty-row numbering.
func (b *book) rows(index int, visit func(int, row) error) error {
	var cells row
	var value, inline strings.Builder
	var column, kind string
	var inCell, inValue, inText, formula, cached bool
	ordinal := 0
	return b.tokens(b.sheets[index].target, func(token xml.Token) error {
		switch t := token.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "row":
				b.budget.rows++
				if b.budget.rows > maxRows || cells != nil {
					return fmt.Errorf("too many or nested worksheet rows")
				}
				cells = row{}
			case "c":
				b.budget.cells++
				if cells == nil || inCell || b.budget.cells > maxCells {
					return fmt.Errorf("too many or misplaced worksheet cells")
				}
				ref := attr(t, "r")
				n := strings.IndexFunc(ref, func(r rune) bool { return r < 'A' || r > 'Z' })
				if n < 1 || n > 3 {
					return fmt.Errorf("invalid cell reference %q", ref)
				}
				if _, err := strconv.Atoi(ref[n:]); err != nil {
					return fmt.Errorf("invalid cell reference %q", ref)
				}
				column, kind = ref[:n], attr(t, "t")
				inCell, formula, cached = true, false, false
				value.Reset()
				inline.Reset()
			case "v":
				inValue, cached = inCell, inCell
			case "t":
				inText = inCell && kind == "inlineStr"
			case "f":
				formula = inCell
			}
		case xml.CharData:
			if inValue {
				if value.Len()+len(t) > maxCellBytes {
					return fmt.Errorf("cell value exceeds 64 KiB")
				}
				value.Write(t)
			}
			if inText {
				if inline.Len()+len(t) > maxCellBytes {
					return fmt.Errorf("inline string exceeds 64 KiB")
				}
				inline.Write(t)
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "v":
				inValue = false
			case "t":
				inText = false
			case "c":
				if formula && !cached {
					return fmt.Errorf("cell %s has a formula without a cached value; save the workbook in Excel first", column)
				}
				v := value.String()
				if kind == "s" && v != "" {
					i, err := strconv.Atoi(v)
					if err != nil || i < 0 || i >= len(b.strings) {
						return fmt.Errorf("invalid shared string index %q", v)
					}
					v = b.strings[i]
				} else if kind == "inlineStr" {
					v = inline.String()
				}
				if v = strings.TrimSpace(v); v != "" {
					if _, exists := cells[column]; exists {
						return fmt.Errorf("duplicate cell column %s", column)
					}
					cells[column] = v
				}
				inCell = false
			case "row":
				if len(cells) > 0 {
					ordinal++
					if visit != nil {
						if err := visit(ordinal, cells); err != nil {
							return fmt.Errorf("row %d: %w", ordinal, err)
						}
					}
				}
				cells = nil
			}
		}
		return nil
	})
}
