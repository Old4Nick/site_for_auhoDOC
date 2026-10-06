package assets

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"equipment-act/internal/money"
)

type Input struct {
	EquipmentType   string `json:"equipment_type"`
	Model           string `json:"model"`
	InventoryNumber string `json:"inventory_number"`
	SerialNumber    string `json:"serial_number"`
	ReceivedDate    string `json:"received_date"`
	CurrentHolder   string `json:"current_holder"`
	PriceRub        string `json:"price_rub"`
	VATMode         string `json:"vat_mode"`
	VATRate         string `json:"vat_rate"`
	VATRub          string `json:"vat_rub"`
}

type Asset struct {
	Input
	ID          string `json:"id"`
	CostDisplay string `json:"cost_display"`
	Revision    string `json:"revision"`
	PriceMinor  int64  `json:"-"`
	VATMinor    int64  `json:"-"`
}

type Prepared struct {
	Input
	PriceMinor int64
	VATMinor   int64
	Received   *time.Time
}

type ValidationError struct {
	Fields map[string]string `json:"fields"`
}

func (e *ValidationError) Error() string {
	return "проверьте заполнение полей оборудования"
}

// NormalizeNumber only trims boundary whitespace and folds case. Internal
// characters, leading zeros, punctuation and serial identities remain intact.
func NormalizeNumber(value string) string { return strings.ToLower(strings.TrimSpace(value)) }

func Validate(input Input) (Prepared, error) {
	p := Prepared{Input: input}
	p.EquipmentType = strings.TrimSpace(p.EquipmentType)
	p.Model = strings.TrimSpace(p.Model)
	p.InventoryNumber = strings.TrimSpace(p.InventoryNumber)
	p.SerialNumber = strings.TrimSpace(p.SerialNumber)
	p.CurrentHolder = strings.TrimSpace(p.CurrentHolder)
	fields := map[string]string{}
	for _, field := range []struct {
		name, value string
		required    bool
		max         int
	}{
		{"equipment_type", p.EquipmentType, true, 200}, {"model", p.Model, true, 500},
		{"inventory_number", p.InventoryNumber, true, 200}, {"serial_number", p.SerialNumber, false, 200},
		{"current_holder", p.CurrentHolder, false, 500},
	} {
		if field.required && field.value == "" {
			fields[field.name] = "обязательное поле"
		}
		if !utf8.ValidString(field.value) || strings.ContainsRune(field.value, 0) {
			fields[field.name] = "недопустимые символы"
		}
		if utf8.RuneCountInString(field.value) > field.max {
			fields[field.name] = fmt.Sprintf("не более %d символов", field.max)
		}
	}
	var err error
	p.PriceMinor, err = money.Parse(p.PriceRub)
	if err != nil {
		fields["price_rub"] = err.Error()
	} else {
		p.PriceRub = money.Decimal(p.PriceMinor)
	}
	if p.VATRub == "" && p.VATMode == "none" {
		p.VATRub = "0.00"
	}
	p.VATMinor, err = money.Parse(p.VATRub)
	if err != nil {
		fields["vat_rub"] = err.Error()
	} else {
		p.VATRub = money.Decimal(p.VATMinor)
	}
	if p.VATMinor > p.PriceMinor {
		fields["vat_rub"] = "сумма НДС не может превышать итоговую стоимость"
	}
	switch p.VATMode {
	case "none":
		if p.VATRate != "" {
			fields["vat_rate"] = "для режима «Без НДС» ставку оставьте пустой"
		}
		if p.VATMinor != 0 {
			fields["vat_rub"] = "для режима «Без НДС» сумма НДС должна быть 0.00"
		}
	case "included":
		p.VATRate, err = money.ParseRate(p.VATRate)
		if err != nil {
			fields["vat_rate"] = err.Error()
		}
	default:
		fields["vat_mode"] = "выберите «В том числе НДС» или «Без НДС»"
	}
	if p.ReceivedDate != "" {
		date, e := time.Parse("2006-01-02", p.ReceivedDate)
		if e != nil || date.Year() < 1 || date.Year() > 9999 {
			fields["received_date"] = "укажите существующую дату в формате ГГГГ-ММ-ДД"
		} else {
			p.Received = &date
		}
	}
	if len(fields) != 0 {
		return Prepared{}, &ValidationError{Fields: fields}
	}
	return p, nil
}
