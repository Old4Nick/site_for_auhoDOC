// docx-samples creates only fictional acceptance fixtures in output/.
package main

import (
	"equipment-act/internal/assets"
	"equipment-act/internal/docgen"
	"fmt"
	"os"
)

func main() {
	if err := os.MkdirAll("output/stage-c", 0755); err != nil {
		panic(err)
	}
	for _, n := range []int{1, 3, 18} {
		act := docgen.Act{Number: fmt.Sprintf("ТЕСТ-C-%02d", n), Date: "2026-10-05", Recipient: "Иванов Иван Иванович"}
		for i := 1; i <= n; i++ {
			a := assets.Asset{Input: assets.Input{EquipmentType: "Ноутбук", Model: "Испытательная модель & <Тест> с расширенной комплектацией для рабочего места сотрудника", InventoryNumber: fmt.Sprintf("ТЕСТ-ИНВ-%03d", i), SerialNumber: fmt.Sprintf("СН-2026-%03d", i), VATMode: "included", VATRate: "20"}, PriceMinor: 2760000, VATMinor: 460000}
			if i%3 == 0 {
				a.VATMode = "none"
				a.VATRate = ""
				a.VATMinor = 0
			}
			act.Items = append(act.Items, a)
		}
		data, err := docgen.Generate(act)
		if err != nil {
			panic(err)
		}
		path := fmt.Sprintf("output/stage-c/act-%02d.docx", n)
		if err := os.WriteFile(path, data, 0600); err != nil {
			panic(err)
		}
		fmt.Println(path)
	}
}
