package docgen

import (
	"archive/zip"
	"bytes"
	"equipment-act/internal/assets"
	acttemplate "equipment-act/templates/act"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
)

func fixture(n int) Act {
	a := Act{Number: "Акт & <1>", Date: "2026-10-05", Recipient: "Иванов Иван"}
	for i := 0; i < n; i++ {
		item := assets.Asset{Input: assets.Input{EquipmentType: "Ноутбук", Model: "Кириллица & <длинная модель>", InventoryNumber: fmt.Sprintf("ИНВ-%03d", i), VATMode: "included", VATRate: "20"}, PriceMinor: 2760000, VATMinor: 460000}
		if i%2 == 1 {
			item.VATMode = "none"
			item.VATMinor = 0
		}
		a.Items = append(a.Items, item)
	}
	return a
}

func parts(t *testing.T, data []byte) map[string][]byte {
	t.Helper()
	z, e := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if e != nil {
		t.Fatal(e)
	}
	result := map[string][]byte{}
	for _, f := range z.File {
		r, e := f.Open()
		if e != nil {
			t.Fatal(e)
		}
		b, e := io.ReadAll(r)
		r.Close()
		if e != nil {
			t.Fatal(e)
		}
		result[f.Name] = b
	}
	return result
}

func TestDocuments(t *testing.T) {
	source := parts(t, acttemplate.Bytes)
	for _, n := range []int{1, 3, 18, 100} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			data, e := Generate(fixture(n))
			if e != nil {
				t.Fatal(e)
			}
			got := parts(t, data)
			xml := string(got["word/document.xml"])
			if len(rowPattern.FindAllString(xml, -1)) != n+2 {
				t.Fatal("wrong number of equipment rows")
			}
			for _, text := range []string{"Иванов Иван", "05.10.2026", "&amp;", "&lt;", "НДС 20%", "4 600 руб. 00 коп."} {
				if !strings.Contains(xml, text) {
					t.Fatalf("missing %s", text)
				}
			}
			if n == 3 && !strings.Contains(xml, "82 800 руб. 00 коп.") {
				t.Fatal("wrong total")
			}
			previous := -1
			for i := 0; i < n; i++ {
				next := strings.Index(xml, fmt.Sprintf("ИНВ-%03d", i))
				if next <= previous {
					t.Fatal("order lost")
				}
				previous = next
			}
			for name, original := range source {
				if name != "word/document.xml" && !bytes.Equal(got[name], original) {
					t.Fatalf("unrelated part changed: %s", name)
				}
			}
		})
	}
}

func TestSplitRowMarkers(t *testing.T) {
	split := `<w:p><w:r><w:t>{RO</w:t></w:r><w:r><w:t>W_NO}</w:t></w:r><w:r><w:t>{NA</w:t></w:r><w:r><w:t>ME}</w:t></w:r></w:p>`
	result := normalizeRowMarkers(split)
	if !strings.Contains(result, "{ROW_NO}") || !strings.Contains(result, "{NAME}") {
		t.Fatal(result)
	}
	if strings.Count(result, "<w:r>") != 4 {
		t.Fatal("run structure changed")
	}
}

func TestIndependentConcurrentDocuments(t *testing.T) {
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			a := fixture(3)
			a.Recipient = fmt.Sprintf("Получатель_%d", i)
			b, e := Generate(a)
			if e != nil {
				t.Error(e)
				return
			}
			if !bytes.Contains(parts(t, b)["word/document.xml"], []byte(a.Recipient)) {
				t.Error("recipient mixed")
			}
		}(i)
	}
	wg.Wait()
}

func TestInvalidDocument(t *testing.T) {
	for _, a := range []Act{{}, {Number: "1", Date: "2026-02-30", Recipient: "Иванов", Items: fixture(1).Items}, fixture(101)} {
		if _, e := Generate(a); e == nil {
			t.Fatal("invalid act accepted")
		}
	}
	if e := verify([]byte("not a zip")); e == nil {
		t.Fatal("invalid zip accepted")
	}
}
