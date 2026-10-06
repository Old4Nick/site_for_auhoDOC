package docgen

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"equipment-act/internal/assets"
	"equipment-act/internal/money"
	acttemplate "equipment-act/templates/act"
	docx "github.com/lukasjarosch/go-docx"
)

const maxDocumentBytes = 20 << 20

type Act struct {
	Number    string
	Date      string
	Recipient string
	Items     []assets.Asset
}

var rowPattern = regexp.MustCompile(`(?s)<w:tr(?:\s[^>]*)?>.*?</w:tr>`)
var unresolvedPattern = regexp.MustCompile(`\{[A-Z][A-Z0-9_]*\}`)

// go-docx v0.5.0 resets package-level counters when opening a document.
var libraryMutex sync.Mutex

func Generate(input Act) ([]byte, error) {
	if len(input.Items) == 0 || len(input.Items) > 100 {
		return nil, errors.New("Выберите от 1 до 100 устройств.")
	}
	if strings.TrimSpace(input.Number) == "" || strings.TrimSpace(input.Recipient) == "" {
		return nil, errors.New("Укажите номер акта и получателя.")
	}
	date, err := time.Parse("2006-01-02", input.Date)
	if err != nil || date.Format("2006-01-02") != input.Date {
		return nil, errors.New("Укажите дату акта.")
	}
	source := acttemplate.Bytes
	prepared, err := expandRows(source, len(input.Items))
	if err != nil {
		return nil, err
	}
	libraryMutex.Lock()
	defer libraryMutex.Unlock()
	document, err := docx.OpenBytes(prepared)
	if err != nil {
		return nil, fmt.Errorf("не удалось открыть рабочий шаблон: %w", err)
	}
	defer document.Close()
	replacements := docx.PlaceholderMap{
		"ACT_NUMBER": input.Number,
		"DATE":       date.Format("02.01.2006"),
		"RECIPIENT":  input.Recipient,
	}
	var total int64
	vatByRate := map[string]int64{}
	var noVAT int64
	hasNoVAT := false
	for i, item := range input.Items {
		if item.PriceMinor < 0 || item.VATMinor < 0 {
			return nil, errors.New("Некорректная стоимость устройства.")
		}
		total, err = money.Add(total, item.PriceMinor)
		if err != nil {
			return nil, err
		}
		if item.VATMode == "none" {
			hasNoVAT = true
			noVAT, err = money.Add(noVAT, item.PriceMinor)
		} else {
			vatByRate[item.VATRate], err = money.Add(vatByRate[item.VATRate], item.VATMinor)
		}
		if err != nil {
			return nil, err
		}
		suffix := "_" + strconv.Itoa(i+1)
		replacements["ROW_NO"+suffix] = strconv.Itoa(i + 1)
		replacements["NAME"+suffix] = strings.TrimSpace(item.EquipmentType + " " + item.Model)
		replacements["INV"+suffix] = item.InventoryNumber
		serial := item.SerialNumber
		if serial == "" {
			serial = "—"
		}
		replacements["SN"+suffix] = serial
		replacements["COUNT"+suffix] = "1"
		replacements["PRICE"+suffix] = money.Cost(item.PriceMinor, item.VATMinor, item.VATMode, item.VATRate)
	}
	lines := []string{money.Format(total)}
	rates := make([]string, 0, len(vatByRate))
	for rate := range vatByRate {
		rates = append(rates, rate)
	}
	sort.Strings(rates)
	for _, rate := range rates {
		lines = append(lines, "в т. ч. НДС "+rate+"% — "+money.Format(vatByRate[rate]))
	}
	if hasNoVAT {
		lines = append(lines, "без НДС — "+money.Format(noVAT))
	}
	replacements["COMMON_PRICE"] = strings.Join(lines, "; ")
	markers, err := document.GetPlaceHoldersList()
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, marker := range markers {
		key := strings.TrimSuffix(strings.TrimPrefix(marker, "{"), "}")
		if _, ok := replacements[key]; !ok {
			return nil, errors.New("В шаблоне найден неизвестный маркер.")
		}
		seen[key] = true
	}
	for key := range replacements {
		if !seen[key] {
			return nil, fmt.Errorf("В шаблоне отсутствует маркер %s.", key)
		}
	}
	if err := document.ReplaceAll(replacements); err != nil {
		return nil, fmt.Errorf("не удалось заполнить шаблон Word: %w", err)
	}
	var result bytes.Buffer
	if err := document.Write(&result); err != nil {
		return nil, fmt.Errorf("не удалось собрать документ Word: %w", err)
	}
	if result.Len() > maxDocumentBytes {
		return nil, errors.New("Документ превышает допустимый размер.")
	}
	if err := verify(result.Bytes()); err != nil {
		return nil, err
	}
	return result.Bytes(), nil
}

// expandRows edits only the single known equipment row in this template.
func expandRows(source []byte, count int) ([]byte, error) {
	reader, err := zip.NewReader(bytes.NewReader(source), int64(len(source)))
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	writer := zip.NewWriter(&out)
	found := false
	for _, file := range reader.File {
		r, err := file.Open()
		if err != nil {
			return nil, err
		}
		content, err := io.ReadAll(r)
		r.Close()
		if err != nil {
			return nil, err
		}
		if file.Name == "word/document.xml" {
			xml := normalizeRowMarkers(string(content))
			rows := rowPattern.FindAllStringIndex(xml, -1)
			match := -1
			for _, row := range rows {
				if strings.Contains(xml[row[0]:row[1]], "{ROW_NO}") {
					if match >= 0 {
						return nil, errors.New("Шаблон содержит несколько строк оборудования.")
					}
					match = row[0]
				}
			}
			if match < 0 {
				return nil, errors.New("Строка оборудования в шаблоне не найдена.")
			}
			var original string
			var end int
			for _, row := range rows {
				if row[0] == match {
					original = xml[row[0]:row[1]]
					end = row[1]
					break
				}
			}
			for _, marker := range []string{"ROW_NO", "NAME", "INV", "SN", "COUNT", "PRICE"} {
				if strings.Count(original, "{"+marker+"}") != 1 {
					return nil, fmt.Errorf("Неверный маркер %s в строке оборудования.", marker)
				}
			}
			var all strings.Builder
			for i := 1; i <= count; i++ {
				row := original
				for _, marker := range []string{"ROW_NO", "NAME", "INV", "SN", "COUNT", "PRICE"} {
					row = strings.ReplaceAll(row, "{"+marker+"}", "{"+marker+"_"+strconv.Itoa(i)+"}")
				}
				if i == count {
					row = strings.ReplaceAll(row, "<w:pPr>", "<w:pPr><w:keepNext/>")
				}
				all.WriteString(row)
			}
			content = []byte(xml[:match] + all.String() + xml[end:])
			found = true
		}
		header := file.FileHeader
		w, err := writer.CreateHeader(&header)
		if err != nil {
			return nil, err
		}
		if _, err := w.Write(content); err != nil {
			return nil, err
		}
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	if !found {
		return nil, errors.New("В шаблоне нет основного документа.")
	}
	return out.Bytes(), nil
}

func verify(data []byte) error {
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return err
	}
	for _, file := range reader.File {
		if file.Name != "word/document.xml" && !strings.HasPrefix(file.Name, "word/header") && !strings.HasPrefix(file.Name, "word/footer") {
			continue
		}
		r, err := file.Open()
		if err != nil {
			return err
		}
		content, err := io.ReadAll(r)
		r.Close()
		if err != nil {
			return err
		}
		decoder := xml.NewDecoder(bytes.NewReader(content))
		for {
			_, e := decoder.Token()
			if e == io.EOF {
				break
			}
			if e != nil {
				return fmt.Errorf("некорректный XML документа: %w", e)
			}
		}
		if unresolvedPattern.Match(content) {
			return errors.New("В документе остались незаполненные маркеры.")
		}
	}
	return nil
}

var paragraphPattern = regexp.MustCompile(`(?s)<w:p(?:\s[^>]*)?>.*?</w:p>`)
var textPattern = regexp.MustCompile(`(?s)<w:t(?:\s[^>]*)?>(.*?)</w:t>`)

// Join only known marker characters across Word runs; retain every XML tag and
// all run properties. Values are still filled exclusively by go-docx.
func normalizeRowMarkers(source string) string {
	return paragraphPattern.ReplaceAllStringFunc(source, func(p string) string {
		for _, key := range []string{"ROW_NO", "NAME", "INV", "SN", "COUNT", "PRICE"} {
			marker := "{" + key + "}"
			spans := textPattern.FindAllStringSubmatchIndex(p, -1)
			var joined strings.Builder
			for _, s := range spans {
				joined.WriteString(p[s[2]:s[3]])
			}
			start := strings.Index(joined.String(), marker)
			if start < 0 {
				continue
			}
			end := start + len(marker)
			offset := 0
			type edit struct {
				start, end int
				value      string
			}
			edits := []edit{}
			for _, s := range spans {
				length := s[3] - s[2]
				lo, hi := max(start-offset, 0), min(end-offset, length)
				if lo < hi {
					value := ""
					if offset+lo == start {
						value = marker
					}
					edits = append(edits, edit{s[2] + lo, s[2] + hi, value})
				}
				offset += length
			}
			for i := len(edits) - 1; i >= 0; i-- {
				e := edits[i]
				p = p[:e.start] + e.value + p[e.end:]
			}
		}
		return p
	})
}
