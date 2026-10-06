package assets

import "testing"

func validInput() Input {
	return Input{EquipmentType: " Ноутбук ", Model: " Модель ", InventoryNumber: "\tDEMO-001\u00a0", PriceRub: "27600.00", VATMode: "included", VATRate: "22.00", VATRub: "4977.05"}
}

func TestValidationKeepsReportedVAT(t *testing.T) {
	p, err := Validate(validInput())
	if err != nil {
		t.Fatal(err)
	}
	if p.VATMinor != 497705 || p.VATRate != "22" {
		t.Fatalf("VAT changed: %+v", p)
	}
	if p.EquipmentType != "Ноутбук" || p.Model != "Модель" || p.InventoryNumber != "DEMO-001" {
		t.Fatalf("normalization failed: %+v", p)
	}
	if p.Received != nil || p.CurrentHolder != "" {
		t.Fatal("missing facts invented")
	}
	if NormalizeNumber(" \tДЕМО-00 01\u3000") != "демо-00 01" {
		t.Fatal("identity normalization failed")
	}
}

func TestVATDateAndMandatoryRules(t *testing.T) {
	cases := []struct {
		name   string
		change func(*Input)
		field  string
	}{
		{"missing model", func(p *Input) { p.Model = " " }, "model"},
		{"too much VAT", func(p *Input) { p.VATRub = "27600.01" }, "vat_rub"},
		{"no rate", func(p *Input) { p.VATRate = "" }, "vat_rate"},
		{"none with rate", func(p *Input) { p.VATMode = "none"; p.VATRub = "0.00" }, "vat_rate"},
		{"none with tax", func(p *Input) { p.VATMode = "none"; p.VATRate = "" }, "vat_rub"},
		{"bad date", func(p *Input) { p.ReceivedDate = "2026-02-29" }, "received_date"},
		{"year zero", func(p *Input) { p.ReceivedDate = "0000-01-01" }, "received_date"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := validInput()
			tc.change(&in)
			_, err := Validate(in)
			e, ok := err.(*ValidationError)
			if !ok || e.Fields[tc.field] == "" {
				t.Fatalf("missing %s validation: %v", tc.field, err)
			}
		})
	}
	p := validInput()
	p.VATRate = "0"
	p.VATRub = "0.00"
	if _, err := Validate(p); err != nil {
		t.Fatal(err)
	}
	p.VATMode = "none"
	p.VATRate = ""
	p.VATRub = ""
	if _, err := Validate(p); err != nil {
		t.Fatal(err)
	}
}
