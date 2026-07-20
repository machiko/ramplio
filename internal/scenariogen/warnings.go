package scenariogen

import (
	"fmt"
	"regexp"
)

// dataTokenRE matches {{data.KEY}} references in step paths and bodies, mirroring
// the runtime template contract in internal/scenarios/template.go.
var dataTokenRE = regexp.MustCompile(`\{\{\s*data\.([^}\s]+)\s*\}\}`)

// DataParamWarnings cross-checks the declared data columns against the {{data.X}}
// tokens actually referenced in the steps. It surfaces two silent failure modes
// at generation time: a reference to an undeclared column (which fails at run
// time on every request) and a declared column that no step uses (which makes
// the whole data file a no-op). Warnings are advisory, never blocking.
func DataParamWarnings(steps []Step, cols []DataColumn) []string {
	declared := make(map[string]bool, len(cols))
	for _, c := range cols {
		declared[c.Name] = true
	}

	referenced := make(map[string]bool)
	var refOrder []string
	for _, s := range steps {
		for _, src := range []string{s.Path, s.Body} {
			for _, m := range dataTokenRE.FindAllStringSubmatch(src, -1) {
				if key := m[1]; !referenced[key] {
					referenced[key] = true
					refOrder = append(refOrder, key)
				}
			}
		}
	}

	var warnings []string
	for _, key := range refOrder {
		if !declared[key] {
			warnings = append(warnings, fmt.Sprintf(
				"步驟中引用了 {{data.%s}}，但沒有宣告這個變動參數欄位——執行時每個 request 都會失敗。", key))
		}
	}
	for _, c := range cols {
		if !referenced[c.Name] {
			warnings = append(warnings, fmt.Sprintf(
				"宣告了變動參數欄位 %q，但沒有任何步驟引用 {{data.%s}}——這個欄位不會有效果。", c.Name, c.Name))
		}
	}
	return warnings
}
